package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"sync"
	"time"

	hostv1 "agentbox/api"
	"agentbox/internal/domain"
	"agentbox/internal/store"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type hostConnection struct {
	hostID   string
	outbound chan *domain.HostCommand
	terminal chan *hostv1.TerminalInput
	cancel   context.CancelCauseFunc
}

type hostHub struct {
	mu          sync.RWMutex
	connections map[string]*hostConnection
	metrics     *metrics
}

func newHostHub(metricSet *metrics) *hostHub {
	return &hostHub{
		connections: make(map[string]*hostConnection),
		metrics:     metricSet,
	}
}

func (h *hostHub) register(connection *hostConnection) {
	h.mu.Lock()
	previous := h.connections[connection.hostID]
	h.connections[connection.hostID] = connection
	if previous == nil {
		h.metrics.hostConnections.Add(1)
	}
	h.mu.Unlock()
	if previous != nil {
		previous.cancel(errors.New("host opened a replacement connection"))
	}
}

func (h *hostHub) unregister(connection *hostConnection) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.connections[connection.hostID] != connection {
		return false
	}
	delete(h.connections, connection.hostID)
	h.metrics.hostConnections.Add(-1)
	return true
}

func (h *hostHub) connected(hostID string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.connections[hostID] != nil
}

func (h *hostHub) dispatch(hostID string, command *domain.HostCommand) bool {
	h.mu.RLock()
	connection := h.connections[hostID]
	if connection == nil {
		h.mu.RUnlock()
		return false
	}
	select {
	case connection.outbound <- command:
		h.metrics.commandsDispatched.Add(1)
		h.mu.RUnlock()
		return true
	default:
		h.mu.RUnlock()
		return false
	}
}

func (h *hostHub) sendTerminal(hostID string, input *hostv1.TerminalInput) bool {
	h.mu.RLock()
	connection := h.connections[hostID]
	if connection == nil {
		h.mu.RUnlock()
		return false
	}
	select {
	case connection.terminal <- input:
		h.mu.RUnlock()
		return true
	default:
		h.mu.RUnlock()
		return false
	}
}

type receivedHostFrame struct {
	frame *hostv1.HostFrame
	err   error
}

func (s *Server) Connect(stream hostv1.HostService_ConnectServer) error {
	first, err := stream.Recv()
	if err != nil {
		return grpcStatus("receive host hello", err)
	}
	hello := first.GetHello()
	if hello == nil || first.GetHostSeq() != 0 || strings.TrimSpace(first.GetFrameId()) == "" {
		return status.Error(codes.InvalidArgument, "the first frame must be a host hello with host_seq zero and a frame_id")
	}

	host, credential, err := s.authenticateHost(stream.Context(), hello)
	if err != nil {
		return err
	}
	persisted, err := s.store.UpsertHost(stream.Context(), host, hello.GetDaemonInstanceId(), hello.GetLastAckedHostSeq())
	if err != nil {
		return storeGRPCStatus("persist host hello", err)
	}

	connectionContext, cancel := context.WithCancelCause(stream.Context())
	connection := &hostConnection{
		hostID:   persisted.ID,
		outbound: make(chan *domain.HostCommand, 256),
		terminal: make(chan *hostv1.TerminalInput, 256),
		cancel:   cancel,
	}
	s.hosts.register(connection)
	lastAcked := hello.GetLastAckedHostSeq()
	defer func() {
		cancel(nil)
		if s.hosts.unregister(connection) {
			offline := persisted
			offline.Status = domain.HostOffline
			ctx, stop := context.WithTimeout(context.WithoutCancel(stream.Context()), 3*time.Second)
			defer stop()
			if _, updateErr := s.store.UpsertHost(ctx, offline, hello.GetDaemonInstanceId(), lastAcked); updateErr != nil {
				s.logError("mark host offline", updateErr, "host_id", persisted.ID)
			}
		}
	}()

	if err := stream.Send(&hostv1.ServerFrame{
		FrameId: uuid.NewString(),
		Payload: &hostv1.ServerFrame_Welcome{Welcome: &hostv1.Welcome{
			HostId:                  persisted.ID,
			Credential:              credential,
			LastAckedHostSeq:        lastAcked,
			HeartbeatIntervalMillis: s.heartbeatInterval.Milliseconds(),
		}},
	}); err != nil {
		return grpcStatus("send host welcome", err)
	}

	pending, err := s.store.PendingHostCommands(connectionContext, persisted.ID, s.hostCommandLimit)
	if err != nil {
		return storeGRPCStatus("load pending host commands", err)
	}
	for index := range pending {
		if err := sendHostCommand(stream, &pending[index]); err != nil {
			return err
		}
		s.metrics.commandsDispatched.Add(1)
	}

	received := make(chan receivedHostFrame, 1)
	go func() {
		for {
			frame, receiveErr := stream.Recv()
			select {
			case received <- receivedHostFrame{frame: frame, err: receiveErr}:
			case <-connectionContext.Done():
				return
			}
			if receiveErr != nil {
				return
			}
		}
	}()

	pingInterval := s.heartbeatInterval
	if pingInterval < time.Second {
		pingInterval = time.Second
	}
	ping := time.NewTicker(pingInterval)
	defer ping.Stop()

	for {
		select {
		case <-connectionContext.Done():
			cause := context.Cause(connectionContext)
			if errors.Is(cause, context.Canceled) || cause == nil {
				return nil
			}
			return status.Error(codes.Aborted, cause.Error())
		case incoming := <-received:
			if incoming.err != nil {
				if errors.Is(incoming.err, io.EOF) {
					return nil
				}
				return grpcStatus("receive host frame", incoming.err)
			}
			ack, processErr := s.processHostFrame(connectionContext, persisted, hello.GetDaemonInstanceId(), incoming.frame, lastAcked)
			if processErr != nil {
				return processErr
			}
			lastAcked = ack
			if err := stream.Send(&hostv1.ServerFrame{
				FrameId: uuid.NewString(),
				Payload: &hostv1.ServerFrame_EventAck{EventAck: &hostv1.EventAck{LastAckedHostSeq: ack}},
			}); err != nil {
				return grpcStatus("send host event ack", err)
			}
		case command := <-connection.outbound:
			if err := sendHostCommand(stream, command); err != nil {
				return err
			}
		case terminal := <-connection.terminal:
			if err := stream.Send(&hostv1.ServerFrame{
				FrameId: uuid.NewString(),
				Payload: &hostv1.ServerFrame_TerminalInput{TerminalInput: terminal},
			}); err != nil {
				return grpcStatus("send terminal input", err)
			}
		case timestamp := <-ping.C:
			if err := stream.Send(&hostv1.ServerFrame{
				FrameId: uuid.NewString(),
				Payload: &hostv1.ServerFrame_Ping{Ping: &hostv1.Ping{UnixMillis: timestamp.UnixMilli()}},
			}); err != nil {
				return grpcStatus("send host ping", err)
			}
		}
	}
}

func (s *Server) authenticateHost(ctx context.Context, hello *hostv1.HostHello) (domain.Host, string, error) {
	if strings.TrimSpace(hello.GetDaemonInstanceId()) == "" {
		return domain.Host{}, "", status.Error(codes.InvalidArgument, "daemon_instance_id is required")
	}
	hostID := strings.TrimSpace(hello.GetHostId())
	credential := strings.TrimSpace(hello.GetCredential())
	enrollmentToken := strings.TrimSpace(hello.GetEnrollmentToken())
	systemHostname := strings.TrimSpace(hello.GetSystemHostname())
	displayName := strings.TrimSpace(hello.GetDisplayName())
	if len(systemHostname) > 255 || strings.ContainsAny(systemHostname, "\r\n\x00") {
		return domain.Host{}, "", status.Error(codes.InvalidArgument, "system_hostname is invalid")
	}
	if len(displayName) > 512 || strings.ContainsAny(displayName, "\r\n\x00") {
		return domain.Host{}, "", status.Error(codes.InvalidArgument, "display_name is invalid")
	}
	if displayName == "" {
		displayName = systemHostname
	}

	var host domain.Host
	if credential == "" {
		if subtle.ConstantTimeCompare([]byte(enrollmentToken), []byte(s.enrollmentToken)) != 1 {
			return domain.Host{}, "", status.Error(codes.Unauthenticated, "invalid enrollment token")
		}
		if hostID == "" {
			hostID = uuid.NewString()
		}
		if _, err := uuid.Parse(hostID); err != nil {
			return domain.Host{}, "", status.Error(codes.InvalidArgument, "host_id must be a UUID")
		}
		hostName := displayName
		if hostName == "" {
			hostName = defaultHostName(hostID)
		}
		host = domain.Host{
			ID:             hostID,
			OrganizationID: s.systemUser.OrganizationID,
			Name:           hostName,
			Slug:           defaultHostName(hostID),
			Status:         domain.HostOnline,
			MaxActiveBoxes: 4,
		}
	} else {
		if hostID == "" || !s.validHostCredential(hostID, credential) {
			return domain.Host{}, "", status.Error(codes.Unauthenticated, "invalid host credential")
		}
		storedHost, err := s.store.GetHostForOrganization(ctx, s.systemUser.OrganizationID, hostID)
		if err != nil {
			return domain.Host{}, "", storeGRPCStatus("load enrolled host", err)
		}
		if storedHost.Status == domain.HostRevoked {
			return domain.Host{}, "", status.Error(codes.PermissionDenied, "host is revoked")
		}
		host = storedHost
		host.Status = domain.HostOnline
		if displayName != "" {
			host.Name = displayName
		}
	}

	host.SystemHostname = systemHostname
	host.OS = hello.GetOs()
	host.Arch = hello.GetArch()
	host.DaemonVersion = hello.GetDaemonVersion()
	host.Runtimes = runtimeNames(hello.GetRuntimes())
	labels, err := json.Marshal(map[string]any{"workspaceRoots": hello.GetWorkspaceRoots()})
	if err != nil {
		return domain.Host{}, "", status.Error(codes.Internal, "host capabilities could not be encoded")
	}
	host.Labels = labels
	return host, s.hostCredential(host.ID), nil
}

func (s *Server) hostCredential(hostID string) string {
	mac := hmac.New(sha256.New, []byte(s.enrollmentToken))
	_, _ = mac.Write([]byte("agentbox-host-v1\x00" + hostID))
	return "v1." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *Server) validHostCredential(hostID, candidate string) bool {
	expected := s.hostCredential(hostID)
	return hmac.Equal([]byte(expected), []byte(candidate))
}

func defaultHostName(hostID string) string {
	compact := strings.ReplaceAll(hostID, "-", "")
	if len(compact) > 12 {
		compact = compact[:12]
	}
	return "host-" + compact
}

func runtimeNames(capabilities []*hostv1.RuntimeCapability) []string {
	seen := make(map[string]struct{}, len(capabilities))
	names := make([]string, 0, len(capabilities))
	for _, capability := range capabilities {
		if capability == nil {
			continue
		}
		name := strings.TrimSpace(capability.GetName())
		if name == "" {
			continue
		}
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	return names
}

func sendHostCommand(stream hostv1.HostService_ConnectServer, command *domain.HostCommand) error {
	if command == nil {
		return nil
	}
	payload := command.Payload
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}
	if err := stream.Send(&hostv1.ServerFrame{
		FrameId: uuid.NewString(),
		Payload: &hostv1.ServerFrame_Command{Command: &hostv1.HostCommand{
			CommandId:         command.ID,
			IdempotencyKey:    command.IdempotencyKey,
			BoxId:             command.BoxID,
			RunId:             command.RunID,
			RuntimeInstanceId: command.RuntimeInstanceID,
			CommandType:       command.CommandType,
			PayloadJson:       append([]byte(nil), payload...),
			CreatedUnixMillis: command.CreatedAt.UnixMilli(),
		}},
	}); err != nil {
		return grpcStatus("send host command", err)
	}
	return nil
}

func (s *Server) processHostFrame(ctx context.Context, host domain.Host, daemonInstanceID string, frame *hostv1.HostFrame, lastAcked uint64) (uint64, error) {
	if frame == nil || strings.TrimSpace(frame.GetFrameId()) == "" {
		return lastAcked, status.Error(codes.InvalidArgument, "host frame and frame_id are required")
	}
	sequence := frame.GetHostSeq()
	if sequence == 0 {
		return lastAcked, status.Error(codes.InvalidArgument, "host_seq must be greater than zero after hello")
	}
	if sequence <= lastAcked {
		return lastAcked, nil
	}
	if sequence != lastAcked+1 {
		return lastAcked, status.Errorf(codes.FailedPrecondition, "host sequence gap: expected %d, received %d", lastAcked+1, sequence)
	}

	switch {
	case frame.GetHeartbeat() != nil:
	case frame.GetCommandAck() != nil:
		if err := s.processCommandAck(ctx, frame.GetCommandAck()); err != nil {
			return lastAcked, err
		}
	case frame.GetRuntimeEvent() != nil:
		if err := s.processRuntimeEvent(ctx, host.ID, frame.GetRuntimeEvent()); err != nil {
			return lastAcked, err
		}
	case frame.GetRuntimeSnapshot() != nil:
		if err := s.processRuntimeSnapshot(ctx, host.ID, frame.GetFrameId(), frame.GetRuntimeSnapshot()); err != nil {
			return lastAcked, err
		}
	case frame.GetArtifactReady() != nil:
		if err := s.processArtifact(ctx, host.ID, frame.GetArtifactReady()); err != nil {
			return lastAcked, err
		}
	case frame.GetTerminalData() != nil:
		s.terminals.publish(frame.GetTerminalData())
	default:
		return lastAcked, status.Error(codes.InvalidArgument, "host frame payload is required")
	}

	if err := s.store.TouchHost(ctx, host.ID, daemonInstanceID, sequence); err != nil {
		return lastAcked, storeGRPCStatus("persist host cursor", err)
	}
	return sequence, nil
}

func (s *Server) processCommandAck(ctx context.Context, ack *hostv1.CommandAck) error {
	if strings.TrimSpace(ack.GetCommandId()) == "" {
		return status.Error(codes.InvalidArgument, "command ack command_id is required")
	}
	var commandStatus string
	switch ack.GetStage() {
	case hostv1.CommandStage_COMMAND_STAGE_ACCEPTED:
		commandStatus = "accepted"
	case hostv1.CommandStage_COMMAND_STAGE_STARTED:
		commandStatus = "started"
	case hostv1.CommandStage_COMMAND_STAGE_COMPLETED:
		commandStatus = "completed"
	case hostv1.CommandStage_COMMAND_STAGE_FAILED:
		commandStatus = "failed"
	case hostv1.CommandStage_COMMAND_STAGE_CANCELLED:
		commandStatus = "cancelled"
	default:
		return status.Error(codes.InvalidArgument, "command ack stage is required")
	}
	if err := s.store.UpdateHostCommand(ctx, ack.GetCommandId(), commandStatus, ack.GetErrorCode(), ack.GetErrorMessage(), ack.GetResultJson()); err != nil {
		return storeGRPCStatus("persist command ack", err)
	}
	s.metrics.observeCommandOutcome(commandStatus)
	return nil
}

func (s *Server) processRuntimeEvent(ctx context.Context, hostID string, runtimeEvent *hostv1.RuntimeEvent) error {
	if strings.TrimSpace(runtimeEvent.GetDaemonEventId()) == "" || strings.TrimSpace(runtimeEvent.GetBoxId()) == "" || strings.TrimSpace(runtimeEvent.GetEventType()) == "" {
		return status.Error(codes.InvalidArgument, "runtime event daemon_event_id, box_id, and event_type are required")
	}
	payload := json.RawMessage(runtimeEvent.GetPayloadJson())
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}
	if !json.Valid(payload) {
		return status.Error(codes.InvalidArgument, "runtime event payload_json is invalid")
	}
	box, err := s.store.GetBoxForHost(ctx, hostID, runtimeEvent.GetBoxId())
	if err != nil {
		return storeGRPCStatus("authorize runtime event", err)
	}
	occurredAt := time.UnixMilli(runtimeEvent.GetOccurredUnixMillis())
	if runtimeEvent.GetOccurredUnixMillis() <= 0 {
		occurredAt = time.Now().UTC()
	}
	event := domain.BoxEvent{
		OrganizationID:    box.OrganizationID,
		BoxID:             box.ID,
		EventID:           stableHostEventID(hostID, runtimeEvent.GetDaemonEventId()),
		HostID:            hostID,
		DaemonEventID:     runtimeEvent.GetDaemonEventId(),
		RunID:             runtimeEvent.GetRunId(),
		RuntimeInstanceID: runtimeEvent.GetRuntimeInstanceId(),
		EventType:         runtimeEvent.GetEventType(),
		ActorKind:         runtimeEvent.GetActorKind(),
		ActorID:           runtimeEvent.GetActorId(),
		Payload:           payload,
		OccurredAt:        occurredAt,
	}
	if event.ActorKind == "" {
		event.ActorKind = "daemon"
	}
	if err := s.appendApplyBroadcast(ctx, event); err != nil {
		return err
	}
	if event.EventType == "runtime.ready" {
		commands, pendingErr := s.store.PendingHostCommands(ctx, hostID, 100)
		if pendingErr != nil {
			return storeGRPCStatus("load runtime-ready commands", pendingErr)
		}
		for index := range commands {
			s.dispatch(&commands[index])
		}
	}
	if terminalRuntimeEvent(event.EventType) {
		_, command, claimErr := s.store.ClaimNextRun(ctx, box.ID)
		if claimErr != nil && !errors.Is(claimErr, store.ErrNotFound) && !errors.Is(claimErr, store.ErrConflict) {
			return storeGRPCStatus("claim queued run", claimErr)
		}
		if command != nil {
			s.dispatch(command)
		}
	}
	return nil
}

func (s *Server) processRuntimeSnapshot(ctx context.Context, hostID, frameID string, snapshot *hostv1.RuntimeSnapshot) error {
	for index, process := range snapshot.GetProcesses() {
		if process == nil || strings.TrimSpace(process.GetBoxId()) == "" {
			return status.Error(codes.InvalidArgument, "runtime snapshot process box_id is required")
		}
		box, err := s.store.GetBoxForHost(ctx, hostID, process.GetBoxId())
		if err != nil {
			return storeGRPCStatus("authorize runtime snapshot", err)
		}
		capabilities := json.RawMessage(process.GetCapabilitiesJson())
		if len(capabilities) == 0 {
			capabilities = json.RawMessage(`{}`)
		}
		if !json.Valid(capabilities) {
			return status.Error(codes.InvalidArgument, "runtime snapshot capabilities_json is invalid")
		}
		payload, err := json.Marshal(map[string]any{
			"runtimeInstanceId": process.GetRuntimeInstanceId(),
			"boxId":             process.GetBoxId(),
			"runtimeType":       process.GetRuntimeType(),
			"runtimeVersion":    process.GetRuntimeVersion(),
			"status":            process.GetStatus(),
			"processId":         process.GetProcessId(),
			"sessionRef":        process.GetSessionRef(),
			"capabilities":      capabilities,
		})
		if err != nil {
			return status.Error(codes.Internal, "runtime snapshot could not be encoded")
		}
		daemonEventKey := fmt.Sprintf("snapshot:%s:%d:%s", frameID, index, process.GetRuntimeInstanceId())
		daemonEventID := stableHostEventID(hostID, daemonEventKey)
		event := domain.BoxEvent{
			OrganizationID:    box.OrganizationID,
			BoxID:             box.ID,
			EventID:           daemonEventID,
			HostID:            hostID,
			DaemonEventID:     daemonEventID,
			RuntimeInstanceID: process.GetRuntimeInstanceId(),
			EventType:         "runtime.snapshot",
			ActorKind:         "daemon",
			ActorID:           hostID,
			Payload:           payload,
			OccurredAt:        time.Now().UTC(),
		}
		if err := s.appendApplyBroadcast(ctx, event); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) processArtifact(ctx context.Context, hostID string, artifact *hostv1.ArtifactReady) error {
	if strings.TrimSpace(artifact.GetBoxId()) == "" || strings.TrimSpace(artifact.GetArtifactId()) == "" {
		return status.Error(codes.InvalidArgument, "artifact box_id and artifact_id are required")
	}
	if strings.TrimSpace(artifact.GetName()) == "" || strings.TrimSpace(artifact.GetPath()) == "" {
		return status.Error(codes.InvalidArgument, "artifact name and path are required")
	}
	digest := strings.ToLower(strings.TrimSpace(artifact.GetSha256()))
	decodedDigest, decodeErr := hex.DecodeString(digest)
	if decodeErr != nil || len(decodedDigest) != sha256.Size {
		return status.Error(codes.InvalidArgument, "artifact sha256 must be a 64-character hexadecimal digest")
	}
	if artifact.GetSizeBytes() > math.MaxInt64 {
		return status.Error(codes.InvalidArgument, "artifact size exceeds the supported range")
	}
	box, err := s.store.GetBoxForHost(ctx, hostID, artifact.GetBoxId())
	if err != nil {
		return storeGRPCStatus("authorize artifact", err)
	}
	payload, err := json.Marshal(artifact)
	if err != nil {
		return status.Error(codes.Internal, "artifact metadata could not be encoded")
	}
	daemonEventID := stableHostEventID(hostID, "artifact:"+artifact.GetArtifactId())
	return s.appendApplyBroadcast(ctx, domain.BoxEvent{
		OrganizationID: box.OrganizationID,
		BoxID:          box.ID,
		EventID:        daemonEventID,
		HostID:         hostID,
		DaemonEventID:  daemonEventID,
		RunID:          artifact.GetRunId(),
		EventType:      "artifact.created",
		ActorKind:      "daemon",
		ActorID:        hostID,
		Payload:        payload,
		OccurredAt:     time.Now().UTC(),
	})
}

func (s *Server) appendApplyBroadcast(ctx context.Context, event domain.BoxEvent) error {
	persisted, err := s.store.AppendEvents(ctx, []domain.BoxEvent{event})
	if err != nil {
		return storeGRPCStatus("append runtime event", err)
	}
	for index := range persisted {
		s.events.publish(persisted[index])
		s.metrics.eventsIngested.Add(1)
	}
	return nil
}

func stableHostEventID(hostID, daemonEventID string) string {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte("agentbox://host/"+hostID+"/event/"+daemonEventID)).String()
}

func terminalRuntimeEvent(eventType string) bool {
	switch eventType {
	case "run.completed", "run.failed", "run.aborted", "run.cancelled", "run.lost":
		return true
	default:
		return false
	}
}

func storeGRPCStatus(operation string, err error) error {
	switch {
	case errors.Is(err, store.ErrNotFound):
		return status.Errorf(codes.NotFound, "%s: resource not found", operation)
	case errors.Is(err, store.ErrForbidden):
		return status.Errorf(codes.PermissionDenied, "%s: forbidden", operation)
	case errors.Is(err, store.ErrConflict), errors.Is(err, store.ErrInvalidState):
		return status.Errorf(codes.FailedPrecondition, "%s: conflicting state", operation)
	case errors.Is(err, store.ErrHostOffline), errors.Is(err, store.ErrRuntimeMissing):
		return status.Errorf(codes.Unavailable, "%s: runtime unavailable", operation)
	default:
		return status.Errorf(codes.Internal, "%s failed", operation)
	}
}

func grpcStatus(operation string, err error) error {
	if err == nil {
		return nil
	}
	if _, ok := status.FromError(err); ok {
		return err
	}
	return status.Errorf(codes.Unavailable, "%s: %v", operation, err)
}

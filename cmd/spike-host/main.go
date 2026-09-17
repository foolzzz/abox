package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	hostv1 "agentbox/api"
	hostclient "agentbox/internal/host"
	runtimeapi "agentbox/internal/runtime"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

const bufferSize = 1 << 20

type fakeHostServer struct {
	hostv1.UnimplementedHostServiceServer

	mu                  sync.Mutex
	connections         int
	lastAcked           uint64
	eventDeliveries     map[string]int
	credentialReconnect bool
	completed           chan struct{}
	completeOnce        sync.Once
}

func newFakeHostServer() *fakeHostServer {
	return &fakeHostServer{
		eventDeliveries: make(map[string]int),
		completed:       make(chan struct{}),
	}
}

func (s *fakeHostServer) Connect(stream grpc.BidiStreamingServer[hostv1.HostFrame, hostv1.ServerFrame]) error {
	helloFrame, err := stream.Recv()
	if err != nil {
		return err
	}
	hello := helloFrame.GetHello()
	if hello == nil || helloFrame.HostSeq != 0 || hello.DaemonInstanceId == "" {
		return status.Error(codes.InvalidArgument, "invalid host hello")
	}

	s.mu.Lock()
	s.connections++
	connectionNumber := s.connections
	lastAcked := s.lastAcked
	if connectionNumber == 1 {
		if hello.EnrollmentToken != "enroll-once" || hello.Credential != "" {
			s.mu.Unlock()
			return status.Error(codes.Unauthenticated, "first connection must enroll")
		}
	} else {
		if hello.Credential != "credential-from-server" || hello.EnrollmentToken != "" {
			s.mu.Unlock()
			return status.Error(codes.Unauthenticated, "reconnect must use stored credential")
		}
		s.credentialReconnect = true
	}
	s.mu.Unlock()

	welcome := &hostv1.Welcome{
		HostId:                  "host-spike",
		LastAckedHostSeq:        lastAcked,
		HeartbeatIntervalMillis: 1_000,
	}
	if connectionNumber == 1 {
		welcome.Credential = "credential-from-server"
	}
	if err := stream.Send(&hostv1.ServerFrame{
		FrameId: fmt.Sprintf("welcome-%d", connectionNumber),
		Payload: &hostv1.ServerFrame_Welcome{Welcome: welcome},
	}); err != nil {
		return err
	}

	command := &hostv1.HostCommand{
		CommandId:         "command-1",
		IdempotencyKey:    "idem-command-1",
		BoxId:             "box-1",
		RunId:             "run-1",
		RuntimeInstanceId: "runtime-1",
		CommandType:       "runtime.prompt",
		PayloadJson:       []byte(`{"message":"hello"}`),
		CreatedUnixMillis: time.Now().UnixMilli(),
	}
	if err := stream.Send(&hostv1.ServerFrame{
		FrameId: "duplicate-command-frame",
		Payload: &hostv1.ServerFrame_Command{Command: command},
	}); err != nil {
		return err
	}

	var sawEvent bool
	var sawStarted bool
	for {
		frame, err := stream.Recv()
		if err != nil {
			return err
		}
		if event := frame.GetRuntimeEvent(); event != nil {
			s.mu.Lock()
			s.eventDeliveries[event.DaemonEventId]++
			sawEvent = true
			s.mu.Unlock()
		}
		if ack := frame.GetCommandAck(); ack != nil {
			if ack.Stage == hostv1.CommandStage_COMMAND_STAGE_STARTED {
				sawStarted = true
			}
			if ack.Stage == hostv1.CommandStage_COMMAND_STAGE_COMPLETED {
				s.mu.Lock()
				deliveries := s.eventDeliveries["event-1"]
				s.mu.Unlock()
				if deliveries >= 2 {
					s.completeOnce.Do(func() { close(s.completed) })
				}
			}
		}

		if connectionNumber == 1 {
			if sawEvent && sawStarted {
				return status.Error(codes.Unavailable, "intentional spike disconnect")
			}
			continue
		}

		s.mu.Lock()
		if frame.HostSeq == s.lastAcked+1 {
			s.lastAcked = frame.HostSeq
		}
		acknowledged := s.lastAcked
		s.mu.Unlock()
		if err := stream.Send(&hostv1.ServerFrame{
			FrameId: fmt.Sprintf("event-ack-%d", acknowledged),
			Payload: &hostv1.ServerFrame_EventAck{EventAck: &hostv1.EventAck{
				LastAckedHostSeq: acknowledged,
			}},
		}); err != nil {
			return err
		}
	}
}

func (s *fakeHostServer) result() (connections int, deliveries int, credentialReconnect bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.connections, s.eventDeliveries["event-1"], s.credentialReconnect
}

type countingHandler struct {
	executions atomic.Int32
}

func (h *countingHandler) HandleCommand(ctx context.Context, command *hostv1.HostCommand) (hostclient.CommandResult, error) {
	h.executions.Add(1)
	select {
	case <-ctx.Done():
		return hostclient.CommandResult{}, ctx.Err()
	case <-time.After(100 * time.Millisecond):
	}
	result, err := json.Marshal(map[string]string{"commandId": command.CommandId, "status": "ok"})
	if err != nil {
		return hostclient.CommandResult{}, err
	}
	return hostclient.CommandResult{
		Stage:      hostv1.CommandStage_COMMAND_STAGE_COMPLETED,
		ResultJSON: result,
	}, nil
}

type snapshotHooks struct {
	welcomeCount atomic.Int32
}

func (h *snapshotHooks) OnWelcome(_ context.Context, welcome *hostv1.Welcome) error {
	if welcome.HostId != "host-spike" {
		return fmt.Errorf("unexpected reconciled host %q", welcome.HostId)
	}
	h.welcomeCount.Add(1)
	return nil
}

func (*snapshotHooks) RuntimeSnapshot(context.Context) (*hostv1.RuntimeSnapshot, error) {
	return &hostv1.RuntimeSnapshot{Processes: []*hostv1.RuntimeProcess{{
		RuntimeInstanceId: "runtime-1",
		BoxId:             "box-1",
		RuntimeType:       "omp",
		RuntimeVersion:    "spike",
		Status:            "ready",
		ProcessId:         int64(os.Getpid()),
		SessionRef:        "session-spike",
	}}}, nil
}

func run() error {
	stateDirectory, err := os.MkdirTemp("", "agentbox-host-spike-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(stateDirectory) }()

	listener := bufconn.Listen(bufferSize)
	grpcServer := grpc.NewServer()
	fakeServer := newFakeHostServer()
	hostv1.RegisterHostServiceServer(grpcServer, fakeServer)
	serverErrors := make(chan error, 1)
	go func() { serverErrors <- grpcServer.Serve(listener) }()
	defer func() {
		grpcServer.Stop()
		_ = listener.Close()
	}()

	connection, err := grpc.NewClient(
		"passthrough:///agentbox-host-spike",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return listener.Dial()
		}),
	)
	if err != nil {
		return err
	}
	defer func() { _ = connection.Close() }()

	var frameNumber atomic.Uint64
	newFrameID := func() string {
		return fmt.Sprintf("host-frame-%06d", frameNumber.Add(1))
	}
	journal, err := hostclient.OpenJournal(filepath.Join(stateDirectory, "outbound.jsonl"), hostclient.DefaultJournalLimits(), newFrameID)
	if err != nil {
		return err
	}
	defer func() { _ = journal.Close() }()
	ackStore, err := hostclient.NewFileAckStore(filepath.Join(stateDirectory, "acks.json"))
	if err != nil {
		return err
	}
	credentialStore, err := hostclient.NewFileCredentialStore(filepath.Join(stateDirectory, "credential.json"))
	if err != nil {
		return err
	}
	idempotencyStore, err := hostclient.OpenFileIdempotencyStore(filepath.Join(stateDirectory, "idempotency.json"), hostclient.DefaultIdempotencyLimits())
	if err != nil {
		return err
	}
	handler := &countingHandler{}
	reconciliation := &snapshotHooks{}
	client, err := hostclient.NewClient(hostclient.ClientConfig{
		Service:          hostv1.NewHostServiceClient(connection),
		Journal:          journal,
		AckStore:         ackStore,
		CredentialStore:  credentialStore,
		IdempotencyStore: idempotencyStore,
		CommandHandler:   handler,
		Reconciliation:   reconciliation,
		EnrollmentToken:  "enroll-once",
		DaemonInstanceID: "daemon-spike",
		DaemonVersion:    "spike",
		Backoff: hostclient.Backoff{
			Initial:    10 * time.Millisecond,
			Maximum:    40 * time.Millisecond,
			Multiplier: 2,
			ResetAfter: time.Second,
		},
		NewFrameID: newFrameID,
	})
	if err != nil {
		return err
	}
	if _, err := client.PublishRuntimeEvent(context.Background(), runtimeapi.Event{
		ID:                "event-1",
		Type:              "message.completed",
		BoxID:             "box-1",
		RunID:             "run-1",
		RuntimeInstanceID: "runtime-1",
		RuntimeSeq:        1,
		ActorKind:         "main_agent",
		ActorID:           "main",
		OccurredAt:        time.Now().UTC(),
		Payload:           json.RawMessage(`{"text":"durable event"}`),
	}); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	clientErrors := make(chan error, 1)
	go func() { clientErrors <- client.Run(ctx) }()

	select {
	case <-fakeServer.completed:
		cancel()
	case err := <-client.AsyncErrors():
		cancel()
		return fmt.Errorf("asynchronous client error: %w", err)
	case err := <-serverErrors:
		cancel()
		return fmt.Errorf("fake server stopped: %w", err)
	case <-ctx.Done():
		return fmt.Errorf("spike timed out: %w", ctx.Err())
	}
	clientErr := <-clientErrors
	if !errors.Is(clientErr, context.Canceled) {
		return fmt.Errorf("client stopped unexpectedly: %w", clientErr)
	}
	connections, deliveries, credentialReconnect := fakeServer.result()
	if connections < 2 {
		return fmt.Errorf("expected reconnect, observed %d connection(s)", connections)
	}
	if deliveries < 2 {
		return fmt.Errorf("expected durable event replay, observed %d delivery", deliveries)
	}
	if !credentialReconnect {
		return errors.New("reconnect did not use the enrolled credential")
	}
	if reconciliations := reconciliation.welcomeCount.Load(); reconciliations < 2 {
		return fmt.Errorf("expected snapshot reconciliation after reconnect, observed %d welcome(s)", reconciliations)
	}
	if executions := handler.executions.Load(); executions != 1 {
		return fmt.Errorf("duplicate command executed %d times", executions)
	}
	fmt.Printf("host protocol spike passed: connections=%d event_deliveries=%d command_executions=%d\n", connections, deliveries, handler.executions.Load())
	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

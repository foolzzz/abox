package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	hostv1 "agentbox/api"
	runtimeapi "agentbox/internal/runtime"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
)

type Backoff struct {
	Initial    time.Duration
	Maximum    time.Duration
	Multiplier uint32
	ResetAfter time.Duration
}

func DefaultBackoff() Backoff {
	return Backoff{
		Initial:    250 * time.Millisecond,
		Maximum:    30 * time.Second,
		Multiplier: 2,
		ResetAfter: 30 * time.Second,
	}
}

func (b Backoff) Delay(attempt uint32) time.Duration {
	if b.Initial <= 0 {
		return 0
	}
	maximum := b.Maximum
	if maximum < b.Initial {
		maximum = b.Initial
	}
	multiplier := b.Multiplier
	if multiplier < 2 {
		multiplier = 2
	}
	delay := b.Initial
	for step := uint32(0); step < attempt && delay < maximum; step++ {
		if delay > maximum/time.Duration(multiplier) {
			return maximum
		}
		delay *= time.Duration(multiplier)
	}
	if delay > maximum {
		return maximum
	}
	return delay
}

type SleepFunc func(ctx context.Context, delay time.Duration) error

type HeartbeatProvider interface {
	Heartbeat(ctx context.Context) (*hostv1.Heartbeat, error)
}

type ReconciliationHooks interface {
	OnWelcome(ctx context.Context, welcome *hostv1.Welcome) error
	RuntimeSnapshot(ctx context.Context) (*hostv1.RuntimeSnapshot, error)
}

type ServerHooks interface {
	OnConfigUpdate(ctx context.Context, update *hostv1.ConfigUpdate) error
}

type ConnectionObserver interface {
	Connected(welcome *hostv1.Welcome)
	Disconnected(err error)
}

type ClientConfig struct {
	Service           hostv1.HostServiceClient
	Journal           *Journal
	AckStore          AckStore
	CredentialStore   CredentialStore
	IdempotencyStore  IdempotencyStore
	CommandHandler    CommandHandler
	Reconciliation    ReconciliationHooks
	ServerHooks       ServerHooks
	HeartbeatProvider HeartbeatProvider
	Observer          ConnectionObserver

	HostID            string
	EnrollmentToken   string
	DaemonInstanceID  string
	DaemonVersion     string
	OS                string
	Arch              string
	Runtimes          []*hostv1.RuntimeCapability
	WorkspaceRoots    []*hostv1.WorkspaceRoot
	HeartbeatInterval time.Duration
	Backoff           Backoff
	NewFrameID        FrameIDGenerator
	Sleep             SleepFunc
}

type Client struct {
	service           hostv1.HostServiceClient
	journal           *Journal
	ackStore          AckStore
	credentialStore   CredentialStore
	idempotency       IdempotencyStore
	handler           CommandHandler
	reconciliation    ReconciliationHooks
	serverHooks       ServerHooks
	heartbeatProvider HeartbeatProvider
	observer          ConnectionObserver

	hostID            string
	enrollmentToken   string
	daemonInstanceID  string
	daemonVersion     string
	os                string
	arch              string
	runtimes          []*hostv1.RuntimeCapability
	workspaceRoots    []*hostv1.WorkspaceRoot
	heartbeatInterval time.Duration
	backoff           Backoff
	newFrameID        FrameIDGenerator
	sleep             SleepFunc

	notify      chan struct{}
	asyncErrors chan error
	fatalErrors chan error
	commandWG   sync.WaitGroup
	runMu       sync.Mutex
	running     bool
}

func NewClient(config ClientConfig) (*Client, error) {
	if config.Service == nil || config.Journal == nil || config.AckStore == nil || config.CredentialStore == nil || config.IdempotencyStore == nil || config.CommandHandler == nil {
		return nil, protocolError("create client", CodeConfiguration, errors.New("service, journal, persistence stores, and command handler are required"))
	}
	if config.DaemonInstanceID == "" {
		return nil, protocolError("create client", CodeConfiguration, errors.New("daemon instance id is required"))
	}
	if config.EnrollmentToken == "" {
		credential, err := config.CredentialStore.Load(context.Background())
		if err != nil {
			return nil, err
		}
		if credential.Secret == "" {
			return nil, protocolError("create client", CodeAuthentication, errors.New("enrollment token or stored credential is required"))
		}
	}
	if config.OS == "" {
		config.OS = runtime.GOOS
	}
	if config.Arch == "" {
		config.Arch = runtime.GOARCH
	}
	if config.HeartbeatInterval <= 0 {
		config.HeartbeatInterval = 15 * time.Second
	}
	if config.HeartbeatInterval < 100*time.Millisecond || config.HeartbeatInterval > 24*time.Hour {
		return nil, protocolError("create client", CodeConfiguration, errors.New("heartbeat interval must be between 100ms and 24h"))
	}
	if config.Backoff.Initial <= 0 {
		config.Backoff = DefaultBackoff()
	}
	if config.Backoff.Maximum < config.Backoff.Initial || config.Backoff.Multiplier < 2 || config.Backoff.ResetAfter <= 0 {
		return nil, protocolError("create client", CodeConfiguration, errors.New("invalid reconnect backoff"))
	}
	if config.NewFrameID == nil {
		config.NewFrameID = uuid.NewString
	}
	if config.Sleep == nil {
		config.Sleep = sleepContext
	}
	lastAcked, err := config.AckStore.Load(context.Background(), config.DaemonInstanceID)
	if err != nil {
		return nil, err
	}
	if err := config.Journal.Prepare(lastAcked); err != nil {
		return nil, err
	}
	return &Client{
		service:           config.Service,
		journal:           config.Journal,
		ackStore:          config.AckStore,
		credentialStore:   config.CredentialStore,
		idempotency:       config.IdempotencyStore,
		handler:           config.CommandHandler,
		reconciliation:    config.Reconciliation,
		serverHooks:       config.ServerHooks,
		heartbeatProvider: config.HeartbeatProvider,
		observer:          config.Observer,
		hostID:            config.HostID,
		enrollmentToken:   config.EnrollmentToken,
		daemonInstanceID:  config.DaemonInstanceID,
		daemonVersion:     config.DaemonVersion,
		os:                config.OS,
		arch:              config.Arch,
		runtimes:          cloneRuntimeCapabilities(config.Runtimes),
		workspaceRoots:    cloneWorkspaceRoots(config.WorkspaceRoots),
		heartbeatInterval: config.HeartbeatInterval,
		backoff:           config.Backoff,
		newFrameID:        config.NewFrameID,
		sleep:             config.Sleep,
		notify:            make(chan struct{}, 1),
		asyncErrors:       make(chan error, 32),
		fatalErrors:       make(chan error, 1),
	}, nil
}

func (c *Client) Run(ctx context.Context) error {
	c.runMu.Lock()
	if c.running {
		c.runMu.Unlock()
		return protocolError("run client", CodeConfiguration, errors.New("client is already running"))
	}
	c.running = true
	c.runMu.Unlock()
	defer func() {
		c.runMu.Lock()
		c.running = false
		c.runMu.Unlock()
	}()

	var attempt uint32
	for {
		if err := ctx.Err(); err != nil {
			c.commandWG.Wait()
			return err
		}
		connectedAt, err := c.runSession(ctx)
		if ctx.Err() != nil {
			c.commandWG.Wait()
			return ctx.Err()
		}
		if permanentProtocolFailure(err) {
			c.commandWG.Wait()
			return err
		}
		if c.observer != nil {
			c.observer.Disconnected(err)
		}
		if connectedAt.IsZero() || time.Since(connectedAt) < c.backoff.ResetAfter {
			if attempt < ^uint32(0) {
				attempt++
			}
		} else {
			attempt = 0
		}
		delayAttempt := attempt
		if delayAttempt > 0 {
			delayAttempt--
		}
		if err := c.sleep(ctx, c.backoff.Delay(delayAttempt)); err != nil {
			c.commandWG.Wait()
			return err
		}
	}
}

func (c *Client) PublishRuntimeEvent(ctx context.Context, event runtimeapi.Event) (uint64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if event.ID == "" || event.BoxID == "" || event.Type == "" || event.OccurredAt.IsZero() {
		return 0, protocolError("publish runtime event", CodeProtocol, errors.New("event id, box id, type, and occurrence time are required"))
	}
	payload := event.Payload
	if len(payload) == 0 {
		payload = json.RawMessage("{}")
	}
	if !json.Valid(payload) {
		return 0, protocolError("publish runtime event", CodeProtocol, errors.New("event payload is not valid JSON"))
	}
	frame, err := c.enqueue(&hostv1.HostFrame{
		Payload: &hostv1.HostFrame_RuntimeEvent{RuntimeEvent: &hostv1.RuntimeEvent{
			DaemonEventId:      event.ID,
			BoxId:              event.BoxID,
			RunId:              event.RunID,
			RuntimeInstanceId:  event.RuntimeInstanceID,
			RuntimeSeq:         event.RuntimeSeq,
			EventType:          event.Type,
			ActorKind:          event.ActorKind,
			ActorId:            event.ActorID,
			OccurredUnixMillis: event.OccurredAt.UnixMilli(),
			PayloadJson:        append([]byte(nil), payload...),
		}},
	})
	if err != nil {
		return 0, err
	}
	return frame.HostSeq, nil
}

func (c *Client) runSession(ctx context.Context) (time.Time, error) {
	sessionCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream, err := c.service.Connect(sessionCtx)
	if err != nil {
		return time.Time{}, protocolError("connect stream", CodeTransport, err)
	}
	lastAcked, err := c.ackStore.Load(sessionCtx, c.daemonInstanceID)
	if err != nil {
		return time.Time{}, err
	}
	if err := c.journal.Prepare(lastAcked); err != nil {
		return time.Time{}, err
	}
	hello, err := c.buildHello(sessionCtx, lastAcked)
	if err != nil {
		return time.Time{}, err
	}
	if err := stream.Send(hello); err != nil {
		return time.Time{}, protocolError("send hello", CodeTransport, err)
	}
	serverFrame, err := stream.Recv()
	if err != nil {
		return time.Time{}, protocolError("receive welcome", CodeTransport, err)
	}
	welcome := serverFrame.GetWelcome()
	if serverFrame.FrameId == "" || welcome == nil {
		return time.Time{}, protocolError("receive welcome", CodeProtocol, errors.New("first server frame must be a welcome with a frame id"))
	}
	if welcome.HostId == "" {
		return time.Time{}, protocolError("receive welcome", CodeProtocol, errors.New("welcome host id is required"))
	}
	if helloHostID := hello.GetHello().HostId; helloHostID != "" && helloHostID != welcome.HostId {
		return time.Time{}, protocolError("receive welcome", CodeAuthentication, fmt.Errorf("server returned host %q for hello host %q", welcome.HostId, helloHostID))
	}
	if welcome.LastAckedHostSeq < lastAcked {
		return time.Time{}, protocolError("receive welcome", CodeProtocol, fmt.Errorf("server ack regressed from %d to %d", lastAcked, welcome.LastAckedHostSeq))
	}
	if welcome.LastAckedHostSeq > c.journal.LastSequence() {
		return time.Time{}, protocolError("receive welcome", CodeProtocol, fmt.Errorf("server ack %d exceeds local sequence %d", welcome.LastAckedHostSeq, c.journal.LastSequence()))
	}
	if hello.GetHello().Credential == "" && welcome.Credential == "" {
		return time.Time{}, protocolError("receive welcome", CodeAuthentication, errors.New("enrollment welcome did not issue a credential"))
	}
	if welcome.Credential != "" {
		if err := c.credentialStore.Save(sessionCtx, Credential{HostID: welcome.HostId, Secret: welcome.Credential}); err != nil {
			return time.Time{}, err
		}
	}
	if welcome.LastAckedHostSeq > lastAcked {
		if err := c.ackStore.Save(sessionCtx, c.daemonInstanceID, welcome.LastAckedHostSeq); err != nil {
			return time.Time{}, err
		}
	}
	if err := c.journal.Acknowledge(welcome.LastAckedHostSeq); err != nil {
		return time.Time{}, err
	}
	if c.reconciliation != nil {
		if err := c.reconciliation.OnWelcome(sessionCtx, proto.Clone(welcome).(*hostv1.Welcome)); err != nil {
			return time.Time{}, protocolError("welcome reconciliation", CodeProtocol, err)
		}
		snapshot, err := c.reconciliation.RuntimeSnapshot(sessionCtx)
		if err != nil {
			return time.Time{}, protocolError("build runtime snapshot", CodeCommand, err)
		}
		if snapshot == nil {
			return time.Time{}, protocolError("build runtime snapshot", CodeProtocol, errors.New("snapshot hook returned nil"))
		}
		if _, err := c.enqueue(&hostv1.HostFrame{Payload: &hostv1.HostFrame_RuntimeSnapshot{RuntimeSnapshot: snapshot}}); err != nil {
			return time.Time{}, err
		}
	}

	connectedAt := time.Now()
	if c.observer != nil {
		c.observer.Connected(proto.Clone(welcome).(*hostv1.Welcome))
	}
	interval := c.heartbeatInterval
	if welcome.HeartbeatIntervalMillis > 0 {
		interval = time.Duration(welcome.HeartbeatIntervalMillis) * time.Millisecond
		if interval < 100*time.Millisecond || interval > 24*time.Hour {
			return time.Time{}, protocolError("receive welcome", CodeProtocol, errors.New("heartbeat interval must be between 100ms and 24h"))
		}
	}
	var sentThrough atomic.Uint64
	sentThrough.Store(welcome.LastAckedHostSeq)
	errCh := make(chan error, 3)
	go func() { errCh <- c.sendLoop(sessionCtx, stream, welcome.LastAckedHostSeq, &sentThrough) }()
	go func() { errCh <- c.receiveLoop(sessionCtx, ctx, stream, &sentThrough) }()
	go func() { errCh <- c.heartbeatLoop(sessionCtx, interval) }()

	var firstErr error
	receivedWorkerResult := false
	select {
	case firstErr = <-errCh:
		receivedWorkerResult = true
	case firstErr = <-c.fatalErrors:
	}
	cancel()
	remaining := 3
	if receivedWorkerResult {
		remaining = 2
	}
	for ; remaining > 0; remaining-- {
		<-errCh
	}
	if ctx.Err() != nil {
		return connectedAt, ctx.Err()
	}
	if firstErr == nil {
		firstErr = io.EOF
	}
	return connectedAt, firstErr
}

func (c *Client) buildHello(ctx context.Context, lastAcked uint64) (*hostv1.HostFrame, error) {
	credential, err := c.credentialStore.Load(ctx)
	if err != nil {
		return nil, err
	}
	hostID := c.hostID
	if credential.HostID != "" {
		if hostID != "" && hostID != credential.HostID {
			return nil, protocolError("build hello", CodeAuthentication, errors.New("configured host id differs from stored credential"))
		}
		hostID = credential.HostID
	}
	if credential.Secret == "" && c.enrollmentToken == "" {
		return nil, protocolError("build hello", CodeAuthentication, errors.New("no enrollment token or credential is available"))
	}
	frameID := c.newFrameID()
	if frameID == "" {
		return nil, protocolError("build hello", CodeConfiguration, errors.New("frame id generator returned an empty id"))
	}
	// Hello is the stream handshake and deliberately uses sequence zero. Every
	// post-handshake frame is assigned a durable positive sequence by Journal.
	return &hostv1.HostFrame{
		FrameId: frameID,
		HostSeq: 0,
		Payload: &hostv1.HostFrame_Hello{Hello: &hostv1.HostHello{
			HostId:           hostID,
			EnrollmentToken:  c.enrollmentTokenIfNeeded(credential),
			Credential:       credential.Secret,
			DaemonInstanceId: c.daemonInstanceID,
			DaemonVersion:    c.daemonVersion,
			Os:               c.os,
			Arch:             c.arch,
			LastAckedHostSeq: lastAcked,
			Runtimes:         cloneRuntimeCapabilities(c.runtimes),
			WorkspaceRoots:   cloneWorkspaceRoots(c.workspaceRoots),
		}},
	}, nil
}

func (c *Client) enrollmentTokenIfNeeded(credential Credential) string {
	if credential.Secret != "" {
		return ""
	}
	return c.enrollmentToken
}

func (c *Client) sendLoop(ctx context.Context, stream hostv1.HostService_ConnectClient, lastSent uint64, sentThrough *atomic.Uint64) error {
	for {
		frames, err := c.journal.EntriesAfter(lastSent)
		if err != nil {
			return err
		}
		for _, frame := range frames {
			// Publish the send watermark first so an immediate server Ack cannot
			// race the goroutine between Send returning and the atomic store.
			sentThrough.Store(frame.HostSeq)
			if err := stream.Send(frame); err != nil {
				return protocolError("send host frame", CodeTransport, err)
			}
			lastSent = frame.HostSeq
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-c.notify:
		}
	}
}

func (c *Client) receiveLoop(sessionCtx, commandCtx context.Context, stream hostv1.HostService_ConnectClient, sentThrough *atomic.Uint64) error {
	for {
		frame, err := stream.Recv()
		if err != nil {
			return protocolError("receive server frame", CodeTransport, err)
		}
		if frame.FrameId == "" || frame.Payload == nil {
			return protocolError("receive server frame", CodeProtocol, errors.New("server frame id and payload are required"))
		}
		if command := frame.GetCommand(); command != nil {
			c.commandWG.Add(1)
			go func(command *hostv1.HostCommand) {
				defer c.commandWG.Done()
				c.dispatchCommand(commandCtx, proto.Clone(command).(*hostv1.HostCommand))
			}(command)
			continue
		}
		if ack := frame.GetEventAck(); ack != nil {
			// EventAck is intrinsically idempotent because the persisted cursor
			// only advances. Avoid retaining one frame ID per heartbeat forever.
			if err := c.processAck(sessionCtx, ack.LastAckedHostSeq, sentThrough.Load()); err != nil {
				return err
			}
			continue
		}
		seen, err := c.idempotency.FrameSeen(sessionCtx, frame.FrameId)
		if err != nil {
			return err
		}
		if seen {
			continue
		}
		if err := c.idempotency.RememberFrame(sessionCtx, frame.FrameId); err != nil {
			return err
		}
		switch payload := frame.Payload.(type) {
		case *hostv1.ServerFrame_ConfigUpdate:
			if c.serverHooks != nil {
				if err := c.serverHooks.OnConfigUpdate(sessionCtx, proto.Clone(payload.ConfigUpdate).(*hostv1.ConfigUpdate)); err != nil {
					return protocolError("apply config update", CodeProtocol, err)
				}
			}
		case *hostv1.ServerFrame_Ping:
			if err := c.queueHeartbeat(sessionCtx); err != nil {
				return err
			}
		case *hostv1.ServerFrame_Welcome:
			return protocolError("receive server frame", CodeProtocol, errors.New("unexpected welcome on established stream"))
		default:
			return protocolError("receive server frame", CodeProtocol, fmt.Errorf("unsupported server frame payload %T", payload))
		}
	}
}

func (c *Client) processAck(ctx context.Context, acknowledged, sentThrough uint64) error {
	current, err := c.ackStore.Load(ctx, c.daemonInstanceID)
	if err != nil {
		return err
	}
	if acknowledged <= current {
		return nil
	}
	if acknowledged > sentThrough {
		return protocolError("process ack", CodeProtocol, fmt.Errorf("server ack %d exceeds sent sequence %d", acknowledged, sentThrough))
	}
	if err := c.ackStore.Save(ctx, c.daemonInstanceID, acknowledged); err != nil {
		return err
	}
	if err := c.journal.Acknowledge(acknowledged); err != nil {
		return err
	}
	return nil
}

func (c *Client) heartbeatLoop(ctx context.Context, interval time.Duration) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := c.queueHeartbeat(ctx); err != nil {
				return err
			}
		}
	}
}

func (c *Client) queueHeartbeat(ctx context.Context) error {
	var heartbeat *hostv1.Heartbeat
	var err error
	if c.heartbeatProvider != nil {
		heartbeat, err = c.heartbeatProvider.Heartbeat(ctx)
		if err != nil {
			return protocolError("collect heartbeat", CodeCommand, err)
		}
	}
	if heartbeat == nil {
		heartbeat = &hostv1.Heartbeat{UnixMillis: time.Now().UnixMilli()}
	} else {
		heartbeat = proto.Clone(heartbeat).(*hostv1.Heartbeat)
		if heartbeat.UnixMillis == 0 {
			heartbeat.UnixMillis = time.Now().UnixMilli()
		}
	}
	_, err = c.enqueue(&hostv1.HostFrame{Payload: &hostv1.HostFrame_Heartbeat{Heartbeat: heartbeat}})
	return err
}

func (c *Client) enqueue(frame *hostv1.HostFrame) (*hostv1.HostFrame, error) {
	stored, err := c.journal.Append(frame)
	if err != nil {
		return nil, err
	}
	select {
	case c.notify <- struct{}{}:
	default:
	}
	return stored, nil
}

func (c *Client) reportNonfatalError(err error) {
	if err == nil {
		return
	}
	select {
	case c.asyncErrors <- err:
	default:
	}
}

func (c *Client) reportAsyncError(err error) {
	if err == nil {
		return
	}
	select {
	case c.asyncErrors <- err:
	default:
	}
	select {
	case c.fatalErrors <- err:
	default:
	}
}

func permanentProtocolFailure(err error) bool {
	var protocolErr *ProtocolError
	if !errors.As(err, &protocolErr) {
		return false
	}
	switch protocolErr.Code {
	case CodeConfiguration, CodeAuthentication, CodeJournalFull, CodeJournalCorrupt, CodeIdempotency:
		return true
	default:
		return false
	}
}

func (c *Client) AsyncErrors() <-chan error { return c.asyncErrors }

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func cloneRuntimeCapabilities(values []*hostv1.RuntimeCapability) []*hostv1.RuntimeCapability {
	cloned := make([]*hostv1.RuntimeCapability, 0, len(values))
	for _, value := range values {
		if value != nil {
			cloned = append(cloned, proto.Clone(value).(*hostv1.RuntimeCapability))
		}
	}
	return cloned
}

func cloneWorkspaceRoots(values []*hostv1.WorkspaceRoot) []*hostv1.WorkspaceRoot {
	cloned := make([]*hostv1.WorkspaceRoot, 0, len(values))
	for _, value := range values {
		if value != nil {
			cloned = append(cloned, proto.Clone(value).(*hostv1.WorkspaceRoot))
		}
	}
	return cloned
}

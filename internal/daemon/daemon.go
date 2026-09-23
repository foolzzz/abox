package daemon

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	hostv1 "agentbox/api"
	hostclient "agentbox/internal/host"
	runtimeapi "agentbox/internal/runtime"
	clauderuntime "agentbox/internal/runtime/claude"
	codexruntime "agentbox/internal/runtime/codex"
	ompruntime "agentbox/internal/runtime/omp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
)

type Daemon struct {
	config Config

	connection *grpc.ClientConn
	journal    *hostclient.Journal
	client     *hostclient.Client
	manager    *Manager
	health     *HealthStatus
	terminal   *TerminalManager
	healthHTTP *http.Server

	closeOnce sync.Once
	closeErr  error
}

func New(config Config) (*Daemon, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	guard, err := NewWorkspaceGuard(config.WorkspaceRoots, config.StateDirectory)
	if err != nil {
		return nil, err
	}
	stateDirectory := guard.statePath
	instanceID, err := LoadOrCreateInstanceID(filepath.Join(stateDirectory, "identity.json"))
	if err != nil {
		return nil, err
	}
	state, err := OpenStateStore(filepath.Join(stateDirectory, "runtimes.json"), config.RuntimeStateRetention)
	if err != nil {
		return nil, err
	}
	idempotency, err := OpenDurableIdempotencyStore(filepath.Join(stateDirectory, "idempotency.json"), hostclient.IdempotencyLimits{
		MaxFrameIDs: config.IdempotencyMaxFrames,
		MaxCommands: config.IdempotencyMaxCommands,
	})
	if err != nil {
		return nil, err
	}
	journal, err := hostclient.OpenJournal(filepath.Join(stateDirectory, "outbound.jsonl"), hostclient.JournalLimits{
		MaxBytes:       config.JournalMaxBytes,
		MaxRecords:     config.JournalMaxRecords,
		MaxRecordBytes: config.JournalMaxRecordBytes,
	}, nil)
	if err != nil {
		return nil, err
	}
	cleanupJournal := true
	defer func() {
		if cleanupJournal {
			_ = journal.Close()
		}
	}()
	ackStore, err := hostclient.NewFileAckStore(filepath.Join(stateDirectory, "acks.json"))
	if err != nil {
		return nil, err
	}
	credentialStore, err := hostclient.NewFileCredentialStore(filepath.Join(stateDirectory, "credential.json"))
	if err != nil {
		return nil, err
	}

	runtimeCandidates := []struct {
		adapter            runtimeapi.Adapter
		binary             string
		staticApprovalMode string
	}{
		{adapter: ompruntime.New(ompruntime.Config{Binary: config.OMPBinary}), binary: config.OMPBinary},
	}
	if config.EnableCodex {
		runtimeCandidates = append(runtimeCandidates, struct {
			adapter            runtimeapi.Adapter
			binary             string
			staticApprovalMode string
		}{
			adapter: codexruntime.New(codexruntime.Config{Binary: config.CodexBinary}),
			binary:  config.CodexBinary,
		})
	}
	if config.EnableClaude {
		claudePolicy, policyErr := clauderuntime.StaticPermissionPolicy(config.ClaudePermissionMode)
		if policyErr != nil {
			return nil, fmt.Errorf("claudePermissionMode: %w", policyErr)
		}
		runtimeCandidates = append(runtimeCandidates, struct {
			adapter            runtimeapi.Adapter
			binary             string
			staticApprovalMode string
		}{
			adapter: clauderuntime.New(
				clauderuntime.WithBinary(config.ClaudeBinary),
				clauderuntime.WithAutoCompleteOnboarding(true),
			),
			binary:             config.ClaudeBinary,
			staticApprovalMode: claudePolicy.Mode,
		})
	}
	registrations := make([]AdapterRegistration, 0, len(runtimeCandidates))
	advertisedRuntimes := make([]*hostv1.RuntimeCapability, 0, len(runtimeCandidates))
	runtimeVersions := make([]string, 0, len(runtimeCandidates))
	runtimeDiagnostics := make([]runtimeDiagnostic, 0, len(runtimeCandidates))
	var probeErrors error
	for _, candidate := range runtimeCandidates {
		probeCtx, cancelProbe := context.WithTimeout(context.Background(), config.RuntimeProbeTime)
		version, capabilities, probeErr := candidate.adapter.Probe(probeCtx)
		cancelProbe()
		if probeErr != nil {
			probeErrors = errors.Join(probeErrors, fmt.Errorf("probe %s runtime: %w", candidate.adapter.Name(), probeErr))
			runtimeDiagnostics = append(runtimeDiagnostics, runtimeDiagnostic{
				Name:    string(candidate.adapter.Name()),
				Summary: "runtime probe failed",
			})
			continue
		}
		capabilitiesJSON, err := json.Marshal(capabilities)
		if err != nil {
			return nil, fmt.Errorf("encode %s capabilities: %w", candidate.adapter.Name(), err)
		}
		binaryPath := candidate.binary
		if resolved, resolveErr := exec.LookPath(candidate.binary); resolveErr == nil {
			if absolute, absoluteErr := filepath.Abs(resolved); absoluteErr == nil {
				binaryPath = absolute
			}
		}
		registrations = append(registrations, AdapterRegistration{
			Adapter:            candidate.adapter,
			Version:            version,
			Capabilities:       capabilities,
			StaticApprovalMode: candidate.staticApprovalMode,
		})
		advertisedRuntimes = append(advertisedRuntimes, &hostv1.RuntimeCapability{
			Name:             string(candidate.adapter.Name()),
			Version:          version,
			BinaryPath:       binaryPath,
			Status:           "available",
			CapabilitiesJson: capabilitiesJSON,
		})
		runtimeVersions = append(runtimeVersions, fmt.Sprintf("%s=%s", candidate.adapter.Name(), version))
		runtimeDiagnostics = append(runtimeDiagnostics, runtimeDiagnostic{
			Name:      string(candidate.adapter.Name()),
			Version:   version,
			Available: true,
		})
	}
	manager, err := NewManager(registrations, guard, state, config.MaxActiveBoxes, config.MaxRunDuration)
	if err != nil {
		return nil, err
	}
	health := NewHealthStatus(manager, len(registrations) > 0, strings.Join(runtimeVersions, ","))
	health.configureDiagnostics(config.DaemonVersion, journal, runtimeDiagnostics)
	if probeErrors != nil {
		health.RecordFailure("runtime_probe", "one or more enabled runtimes failed startup probing")
	}
	target, transportCredentials, err := transport(config)
	if err != nil {
		return nil, err
	}
	connection, err := grpc.NewClient(
		target,
		grpc.WithTransportCredentials(transportCredentials),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{Time: 10 * time.Second, Timeout: 3 * time.Second, PermitWithoutStream: true}),
	)
	if err != nil {
		return nil, fmt.Errorf("create server connection: %w", err)
	}
	cleanupConnection := true
	defer func() {
		if cleanupConnection {
			_ = connection.Close()
		}
	}()
	systemHostname, err := os.Hostname()
	if err != nil {
		return nil, fmt.Errorf("read system hostname: %w", err)
	}

	workspaceRoots := make([]*hostv1.WorkspaceRoot, 0, len(guard.Roots()))
	for _, root := range guard.Roots() {
		workspaceRoots = append(workspaceRoots, &hostv1.WorkspaceRoot{
			Path:     root.Path,
			RealPath: root.RealPath,
			Mode:     "read_write",
		})
	}

	client, err := hostclient.NewClient(hostclient.ClientConfig{
		Service:           hostv1.NewHostServiceClient(connection),
		Journal:           journal,
		AckStore:          ackStore,
		CredentialStore:   credentialStore,
		HostName:          config.HostName,
		SystemHostname:    systemHostname,
		IdempotencyStore:  idempotency,
		CommandHandler:    observedCommandHandler{manager: manager, health: health},
		Reconciliation:    manager,
		ServerHooks:       manager,
		HeartbeatProvider: manager,
		Observer:          health,
		HostID:            config.HostID,
		EnrollmentToken:   config.EnrollmentToken,
		DaemonInstanceID:  instanceID,
		DaemonVersion:     config.DaemonVersion,
		Runtimes:          advertisedRuntimes,
		WorkspaceRoots:    workspaceRoots,
		HeartbeatInterval: config.HeartbeatInterval,
		Backoff:           hostclient.DefaultBackoff(),
		PrepareReconnect: func() {
			connection.ResetConnectBackoff()
			connection.Connect()
		},
	})
	if err != nil {
		return nil, err
	}
	if err := manager.SetPublisher(client); err != nil {
		return nil, err
	}
	client.SetRuntimeSessionHandler(runtimeSessionDiscovery{})
	terminal, err := NewTerminalManager(guard, config.TmuxBinary, config.MaxTerminalSessions, func(data *hostv1.TerminalData) error {
		_, publishErr := client.PublishTerminalData(context.Background(), data)
		return publishErr
	})
	if err != nil {
		return nil, err
	}
	client.SetTerminalHandler(terminal)

	cleanupJournal = false
	cleanupConnection = false
	return &Daemon{
		config:     config,
		connection: connection,
		journal:    journal,
		client:     client,
		manager:    manager,
		terminal:   terminal,
		health:     health,
		healthHTTP: &http.Server{
			Addr:              config.HealthAddress,
			Handler:           health.handler(),
			ReadHeaderTimeout: 5 * time.Second,
			IdleTimeout:       30 * time.Second,
		},
	}, nil
}

func (d *Daemon) Run(ctx context.Context) error {
	if ctx == nil {
		return errors.New("run context is required")
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	listener, err := net.Listen("tcp", d.config.HealthAddress)
	if err != nil {
		return fmt.Errorf("listen on local health endpoint: %w", err)
	}
	healthErrors := make(chan error, 1)
	go func() {
		err := d.healthHTTP.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		healthErrors <- err
	}()

	clientErrors := make(chan error, 1)
	go func() { clientErrors <- d.client.Run(runCtx) }()

	var runErr error
	clientDone := false
	select {
	case <-ctx.Done():
	case err := <-clientErrors:
		clientDone = true
		if err == nil {
			runErr = errors.New("host client stopped unexpectedly")
		} else if !errors.Is(err, context.Canceled) {
			runErr = fmt.Errorf("host client stopped: %w", err)
		}
	case err := <-d.manager.FatalErrors():
		runErr = err
	case err := <-healthErrors:
		if err == nil {
			runErr = errors.New("health endpoint stopped unexpectedly")
		} else {
			runErr = fmt.Errorf("health endpoint stopped: %w", err)
		}
	}
	cancel()

	d.terminal.Close()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), d.config.ShutdownTimeout)
	defer shutdownCancel()
	if err := d.manager.Shutdown(shutdownCtx); err != nil {
		runErr = errors.Join(runErr, fmt.Errorf("stop runtimes: %w", err))
	}
	if err := d.healthHTTP.Shutdown(shutdownCtx); err != nil {
		runErr = errors.Join(runErr, fmt.Errorf("stop health endpoint: %w", err))
	}
	if !clientDone {
		select {
		case err := <-clientErrors:
			if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				runErr = errors.Join(runErr, fmt.Errorf("stop host client: %w", err))
			}
		case <-shutdownCtx.Done():
			runErr = errors.Join(runErr, fmt.Errorf("stop host client: %w", shutdownCtx.Err()))
		}
	}
	if err := d.Close(); err != nil {
		runErr = errors.Join(runErr, err)
	}
	return runErr
}

func (d *Daemon) Close() error {
	d.closeOnce.Do(func() {
		journalErr := d.journal.Close()
		connectionErr := d.connection.Close()
		d.closeErr = errors.Join(journalErr, connectionErr)
	})
	return d.closeErr
}

func transport(config Config) (string, credentials.TransportCredentials, error) {
	target := strings.TrimSpace(config.ServerAddress)
	useTLS := config.ServerTLS
	if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") {
		parsed, err := url.Parse(target)
		if err != nil {
			return "", nil, fmt.Errorf("parse server address: %w", err)
		}
		if parsed.Path != "" && parsed.Path != "/" {
			return "", nil, errors.New("server gRPC address cannot contain a path")
		}
		useTLS = parsed.Scheme == "https"
		target = parsed.Host
	}
	if target == "" {
		return "", nil, errors.New("server gRPC target is empty")
	}
	if !useTLS {
		return target, insecure.NewCredentials(), nil
	}
	serverName := config.ServerName
	if serverName == "" {
		host, _, err := net.SplitHostPort(target)
		if err == nil {
			serverName = host
		}
	}
	return target, credentials.NewTLS(&tls.Config{
		MinVersion: tls.VersionTLS12,
		ServerName: serverName,
	}), nil
}

func EnsureStateEnvironment(config Config) error {
	for label, directory := range map[string]string{
		"daemon home":  config.HomeDirectory,
		"daemon state": config.StateDirectory,
	} {
		path, err := secureStateDirectory(directory)
		if err != nil {
			return fmt.Errorf("secure %s directory: %w", label, err)
		}
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		if info.Mode().Perm() != 0o700 {
			return fmt.Errorf("%s directory permissions are not 0700", label)
		}
	}
	return nil
}

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
	ompruntime "agentbox/internal/runtime/omp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

type Daemon struct {
	config Config

	connection *grpc.ClientConn
	journal    *hostclient.Journal
	client     *hostclient.Client
	manager    *Manager
	health     *HealthStatus
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
	state, err := OpenStateStore(filepath.Join(stateDirectory, "runtimes.json"))
	if err != nil {
		return nil, err
	}
	idempotency, err := OpenDurableIdempotencyStore(filepath.Join(stateDirectory, "idempotency.json"), hostclient.DefaultIdempotencyLimits())
	if err != nil {
		return nil, err
	}
	journal, err := hostclient.OpenJournal(filepath.Join(stateDirectory, "outbound.jsonl"), hostclient.DefaultJournalLimits(), nil)
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

	adapter := ompruntime.New(ompruntime.Config{Binary: config.OMPBinary})
	probeCtx, cancelProbe := context.WithTimeout(context.Background(), config.RuntimeProbeTime)
	runtimeVersion, capabilities, probeErr := adapter.Probe(probeCtx)
	cancelProbe()
	runtimeStatus := "available"
	if probeErr != nil {
		runtimeStatus = "unavailable"
	}
	manager, err := NewManager(adapter, runtimeVersion, capabilities, guard, state, config.MaxActiveBoxes, config.MaxRunDuration)
	if err != nil {
		return nil, err
	}
	health := NewHealthStatus(manager, probeErr == nil, runtimeVersion)
	if probeErr != nil {
		health.SetError(probeErr)
	}

	target, transportCredentials, err := transport(config)
	if err != nil {
		return nil, err
	}
	connection, err := grpc.NewClient(target, grpc.WithTransportCredentials(transportCredentials))
	if err != nil {
		return nil, fmt.Errorf("create server connection: %w", err)
	}
	cleanupConnection := true
	defer func() {
		if cleanupConnection {
			_ = connection.Close()
		}
	}()

	capabilitiesJSON, err := json.Marshal(capabilities)
	if err != nil {
		return nil, fmt.Errorf("encode OMP capabilities: %w", err)
	}
	binaryPath := config.OMPBinary
	if resolved, resolveErr := exec.LookPath(config.OMPBinary); resolveErr == nil {
		if absolute, absoluteErr := filepath.Abs(resolved); absoluteErr == nil {
			binaryPath = absolute
		}
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
		IdempotencyStore:  idempotency,
		CommandHandler:    manager,
		Reconciliation:    manager,
		ServerHooks:       manager,
		HeartbeatProvider: manager,
		Observer:          health,
		HostID:            config.HostID,
		EnrollmentToken:   config.EnrollmentToken,
		DaemonInstanceID:  instanceID,
		DaemonVersion:     config.DaemonVersion,
		Runtimes: []*hostv1.RuntimeCapability{{
			Name:             string(runtimeapi.TypeOMP),
			Version:          runtimeVersion,
			BinaryPath:       binaryPath,
			Status:           runtimeStatus,
			CapabilitiesJson: capabilitiesJSON,
		}},
		WorkspaceRoots:    workspaceRoots,
		HeartbeatInterval: config.HeartbeatInterval,
		Backoff:           hostclient.DefaultBackoff(),
	})
	if err != nil {
		return nil, err
	}
	if err := manager.SetPublisher(client); err != nil {
		return nil, err
	}

	cleanupJournal = false
	cleanupConnection = false
	return &Daemon{
		config:     config,
		connection: connection,
		journal:    journal,
		client:     client,
		manager:    manager,
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
	path, err := secureStateDirectory(config.StateDirectory)
	if err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Mode().Perm() != 0o700 {
		return errors.New("daemon state directory permissions are not 0700")
	}
	return nil
}

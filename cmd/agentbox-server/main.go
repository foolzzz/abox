package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"agentbox/internal/auth"
	"agentbox/internal/config"
	"agentbox/internal/server"
	storepostgres "agentbox/internal/store/postgres"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthv1 "google.golang.org/grpc/health/grpc_health_v1"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)
	configPath := flag.String("config", "", "path to agentbox-server JSON configuration")
	flag.Parse()
	if err := run(logger, *configPath); err != nil {
		logger.Error("agentbox-server stopped", "component", "agentbox-server", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger, configPath string) error {
	rootContext, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	configuration, err := config.Load(configPath)
	if err != nil {
		return err
	}
	dataStore, err := storepostgres.New(rootContext, configuration.DatabaseURL)
	if err != nil {
		return err
	}
	defer dataStore.Close()
	if err := dataStore.Migrate(rootContext); err != nil {
		return err
	}
	defaultAdminHash, err := auth.HashPassword(auth.DefaultAdminPassword)
	if err != nil {
		return err
	}
	systemUser, bootstrapCreated, err := dataStore.EnsureBootstrapAdmin(
		rootContext,
		auth.DefaultAdminUsername,
		"Administrator",
		defaultAdminHash,
	)
	if err != nil {
		return err
	}
	if bootstrapCreated {
		logger.Warn("default administrator created; change the password immediately",
			"component", "agentbox-server", "username", auth.DefaultAdminUsername)
	}

	controlPlane, err := server.New(server.Options{
		Store:                   dataStore,
		SystemUser:              systemUser,
		EnrollmentToken:         configuration.EnrollmentToken,
		ServerVersion:           configuration.Version,
		PublicURL:               configuration.PublicURL,
		StaticDir:               configuration.WebDir,
		Logger:                  logger,
		ApprovalPollInterval:    configuration.ApprovalPollInterval,
		HibernationPollInterval: configuration.HibernationPollInterval,
		SchedulePollInterval:    configuration.SchedulePollInterval,
		ReaperBatchSize:         configuration.ReaperBatchSize,
		RetentionPollInterval:   configuration.RetentionPollInterval,
		OperationalRetention:    configuration.OperationalRetention,
		AuditRetention:          configuration.AuditRetention,
		WebhookSecret:           configuration.WebhookSecret,
		EnableCodex:             configuration.EnableCodex,
		EnableClaude:            configuration.EnableClaude,
		RuntimeModels:           configuration.RuntimeModels,
	})
	if err != nil {
		return err
	}
	defer func() { _ = controlPlane.Close() }()

	httpListener, err := net.Listen("tcp", configuration.HTTPAddr)
	if err != nil {
		return err
	}
	defer func() { _ = httpListener.Close() }()
	grpcListener, err := net.Listen("tcp", configuration.GRPCAddr)
	if err != nil {
		return err
	}
	defer func() { _ = grpcListener.Close() }()

	httpServer := &http.Server{
		Handler:           controlPlane.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       90 * time.Second,
	}
	grpcServer := grpc.NewServer()
	controlPlane.RegisterGRPC(grpcServer)
	healthServer := health.NewServer()
	healthv1.RegisterHealthServer(grpcServer, healthServer)
	healthServer.SetServingStatus("", healthv1.HealthCheckResponse_SERVING)
	healthServer.SetServingStatus("agentbox.v1.HostService", healthv1.HealthCheckResponse_SERVING)

	serveErrors := make(chan error, 2)
	go func() {
		if serveErr := httpServer.Serve(httpListener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			serveErrors <- serveErr
		}
	}()
	go func() {
		if serveErr := grpcServer.Serve(grpcListener); serveErr != nil && !errors.Is(serveErr, grpc.ErrServerStopped) {
			serveErrors <- serveErr
		}
	}()
	go controlPlane.Run(rootContext)

	logger.Info("agentbox-server started",
		"component", "agentbox-server",
		"version", configuration.Version,
		"http_addr", configuration.HTTPAddr,
		"grpc_addr", configuration.GRPCAddr,
		"codex_enabled", configuration.EnableCodex,
		"claude_enabled", configuration.EnableClaude,
	)

	var serveErr error
	select {
	case <-rootContext.Done():
		logger.Info("agentbox-server stopping", "component", "agentbox-server", "reason", "signal")
	case serveErr = <-serveErrors:
		logger.Error("agentbox-server listener stopped", "component", "agentbox-server", "error", serveErr)
		cancel()
	}

	healthServer.SetServingStatus("", healthv1.HealthCheckResponse_NOT_SERVING)
	healthServer.SetServingStatus("agentbox.v1.HostService", healthv1.HealthCheckResponse_NOT_SERVING)
	shutdownContext, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := httpServer.Shutdown(shutdownContext); err != nil && serveErr == nil {
		serveErr = err
	}

	grpcStopped := make(chan struct{})
	go func() {
		grpcServer.GracefulStop()
		close(grpcStopped)
	}()
	select {
	case <-grpcStopped:
	case <-shutdownContext.Done():
		grpcServer.Stop()
		<-grpcStopped
	}
	if serveErr == nil {
		logger.Info("agentbox-server stopped", "component", "agentbox-server")
	}
	return serveErr
}

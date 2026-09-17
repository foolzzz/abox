package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"agentbox/internal/daemon"
)

var version = "dev"

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	if err := run(); err != nil {
		logger.Error("agentboxd stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	config, err := daemon.LoadConfig()
	if err != nil {
		return err
	}
	config.DaemonVersion = version
	if err := daemon.EnsureStateEnvironment(config); err != nil {
		return err
	}
	service, err := daemon.New(config)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := service.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

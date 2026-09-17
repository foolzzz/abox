package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"agentbox/internal/daemon"
)

var version = "dev"

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	configPath := flag.String("config", "", "path to agentboxd JSON configuration")
	flag.Parse()
	if err := run(*configPath); err != nil {
		logger.Error("agentboxd stopped", "error", err)
		os.Exit(1)
	}
}

func run(configPath string) error {
	config, err := daemon.LoadConfig(configPath)
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

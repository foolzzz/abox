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
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil)).With("component", "agentboxd")
	slog.SetDefault(logger)
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
	slog.Info("agentboxd starting",
		"version", version,
		"host_id", config.HostID,
		"host_name", config.HostName,
		"health_addr", config.HealthAddress,
		"max_active_boxes", config.MaxActiveBoxes,
		"codex_enabled", config.EnableCodex,
		"claude_enabled", config.EnableClaude,
	)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := service.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	slog.Info("agentboxd stopped")
	return nil
}

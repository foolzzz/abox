package config

import (
	"os"
	"strconv"
	"time"
)

type Config struct {
	DatabaseURL             string
	HTTPAddr                string
	GRPCAddr                string
	PublicURL               string
	DevUser                 string
	EnrollmentToken         string
	ServerGRPC              string
	WorkspaceRoot           string
	ApprovalPollInterval    time.Duration
	HibernationPollInterval time.Duration
	ReaperBatchSize         int
}

func Load() Config {
	return Config{
		DatabaseURL:             env("AGENTBOX_DATABASE_URL", "postgres://agentbox:agentbox@127.0.0.1:54329/agentbox?sslmode=disable"),
		HTTPAddr:                env("AGENTBOX_HTTP_ADDR", "127.0.0.1:8080"),
		GRPCAddr:                env("AGENTBOX_GRPC_ADDR", "127.0.0.1:9443"),
		PublicURL:               env("AGENTBOX_PUBLIC_URL", "http://127.0.0.1:8080"),
		DevUser:                 env("AGENTBOX_DEV_USER", "owner@example.com"),
		EnrollmentToken:         env("AGENTBOX_ENROLLMENT_TOKEN", "dev-enrollment-token"),
		ServerGRPC:              env("AGENTBOX_SERVER_GRPC", "127.0.0.1:9443"),
		WorkspaceRoot:           env("AGENTBOX_WORKSPACE_ROOT", "/tmp/agentbox-workspaces"),
		ApprovalPollInterval:    durationEnv("AGENTBOX_APPROVAL_POLL", time.Second),
		HibernationPollInterval: durationEnv("AGENTBOX_HIBERNATION_POLL", 30*time.Second),
		ReaperBatchSize:         intEnv("AGENTBOX_REAPER_BATCH", 100),
	}
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func intEnv(key string, fallback int) int {
	value, err := strconv.Atoi(os.Getenv(key))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func durationEnv(key string, fallback time.Duration) time.Duration {
	value, err := time.ParseDuration(os.Getenv(key))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

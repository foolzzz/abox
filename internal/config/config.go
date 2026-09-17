package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Config struct {
	DatabaseURL             string
	HTTPAddr                string
	GRPCAddr                string
	PublicURL               string
	DevelopmentUser         string
	EnableDevelopmentAuth   bool
	EnrollmentToken         string
	WebDir                  string
	Version                 string
	ApprovalPollInterval    time.Duration
	HibernationPollInterval time.Duration
	SchedulePollInterval    time.Duration
	RetentionPollInterval   time.Duration
	OperationalRetention    time.Duration
	AuditRetention          time.Duration
	ReaperBatchSize         int
	WebhookSecret           string
	EnableCodex             bool
	EnableClaude            bool
	RuntimeModels           map[string][]string
}

type fileConfig struct {
	DatabaseURL             string              `json:"databaseUrl"`
	HTTPAddr                string              `json:"httpAddr"`
	GRPCAddr                string              `json:"grpcAddr"`
	PublicURL               string              `json:"publicUrl"`
	DevelopmentUser         string              `json:"developmentUser"`
	EnableDevelopmentAuth   bool                `json:"enableDevelopmentAuth"`
	EnrollmentToken         string              `json:"enrollmentToken"`
	WebDir                  string              `json:"webDir"`
	Version                 string              `json:"version"`
	ApprovalPollInterval    string              `json:"approvalPollInterval"`
	HibernationPollInterval string              `json:"hibernationPollInterval"`
	SchedulePollInterval    string              `json:"schedulePollInterval"`
	RetentionPollInterval   string              `json:"retentionPollInterval"`
	OperationalRetention    string              `json:"operationalRetention"`
	AuditRetention          string              `json:"auditRetention"`
	ReaperBatchSize         int                 `json:"reaperBatchSize"`
	WebhookSecret           string              `json:"webhookSecret"`
	EnableCodex             *bool               `json:"enableCodex"`
	EnableClaude            bool                `json:"enableClaude"`
	RuntimeModels           map[string][]string `json:"runtimeModels"`
}

func DefaultPath() (string, error) {
	if configured := strings.TrimSpace(os.Getenv("AGENTBOX_SERVER_CONFIG")); configured != "" {
		return expandHome(configured)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".agentbox", "server.json"), nil
}

func Load(path string) (Config, error) {
	if strings.TrimSpace(path) == "" {
		var err error
		path, err = DefaultPath()
		if err != nil {
			return Config{}, err
		}
	} else {
		var err error
		path, err = expandHome(path)
		if err != nil {
			return Config{}, err
		}
	}
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Config{}, fmt.Errorf("server config %s does not exist; copy configs/server.example.json and pass --config", path)
		}
		return Config{}, fmt.Errorf("open server config: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return Config{}, fmt.Errorf("stat server config: %w", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return Config{}, fmt.Errorf("server config %s must not be accessible by group or others", path)
	}
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var raw fileConfig
	if err := decoder.Decode(&raw); err != nil {
		return Config{}, fmt.Errorf("decode server config %s: %w", path, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return Config{}, fmt.Errorf("decode server config %s: trailing content", path)
		}
		return Config{}, fmt.Errorf("decode server config %s: %w", path, err)
	}
	approval, err := parseDuration("approvalPollInterval", raw.ApprovalPollInterval)
	if err != nil {
		return Config{}, err
	}
	hibernation, err := parseDuration("hibernationPollInterval", raw.HibernationPollInterval)
	if err != nil {
		return Config{}, err
	}
	schedule, err := parseDuration("schedulePollInterval", raw.SchedulePollInterval)
	if err != nil {
		return Config{}, err
	}
	retentionPoll, err := parseDuration("retentionPollInterval", raw.RetentionPollInterval)
	if err != nil {
		return Config{}, err
	}
	operationalRetention, err := parseDuration("operationalRetention", raw.OperationalRetention)
	if err != nil {
		return Config{}, err
	}
	auditRetention, err := parseDuration("auditRetention", raw.AuditRetention)
	if err != nil {
		return Config{}, err
	}
	webDir, err := expandOptionalPath(raw.WebDir)
	if err != nil {
		return Config{}, err
	}
	enableCodex := true
	if raw.EnableCodex != nil {
		enableCodex = *raw.EnableCodex
	}
	runtimeModels, err := normalizeRuntimeModels(raw.RuntimeModels)
	if err != nil {
		return Config{}, err
	}
	result := Config{
		DatabaseURL: raw.DatabaseURL, HTTPAddr: raw.HTTPAddr, GRPCAddr: raw.GRPCAddr,
		PublicURL: raw.PublicURL, DevelopmentUser: raw.DevelopmentUser,
		EnableDevelopmentAuth: raw.EnableDevelopmentAuth,
		EnrollmentToken:       raw.EnrollmentToken, WebDir: webDir, Version: raw.Version,
		ApprovalPollInterval: approval, HibernationPollInterval: hibernation,
		SchedulePollInterval: schedule, RetentionPollInterval: retentionPoll,
		OperationalRetention: operationalRetention, AuditRetention: auditRetention,
		ReaperBatchSize: raw.ReaperBatchSize, WebhookSecret: raw.WebhookSecret,
		EnableCodex: enableCodex, EnableClaude: raw.EnableClaude,
		RuntimeModels: runtimeModels,
	}
	if err := result.Validate(); err != nil {
		return Config{}, fmt.Errorf("validate server config %s: %w", path, err)
	}
	return result, nil
}

func (c Config) Validate() error {
	for name, value := range map[string]string{
		"databaseUrl": c.DatabaseURL, "httpAddr": c.HTTPAddr, "grpcAddr": c.GRPCAddr,
		"developmentUser": c.DevelopmentUser, "enrollmentToken": c.EnrollmentToken,
		"version": c.Version,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	if c.EnrollmentToken == "replace-me" || c.WebhookSecret == "replace-me" {
		return errors.New("replace placeholder enrollmentToken and webhookSecret before starting the server")
	}
	if strings.TrimSpace(c.WebhookSecret) == "" {
		return errors.New("webhookSecret is required")
	}
	if c.ApprovalPollInterval <= 0 || c.HibernationPollInterval <= 0 || c.SchedulePollInterval <= 0 || c.RetentionPollInterval <= 0 {
		return errors.New("poll intervals must be positive")
	}
	if c.OperationalRetention < time.Hour || c.AuditRetention < time.Hour {
		return errors.New("retention windows must be at least one hour")
	}
	if c.ReaperBatchSize <= 0 || c.ReaperBatchSize > 10_000 {
		return errors.New("reaperBatchSize must be between 1 and 10000")
	}
	return nil
}

func normalizeRuntimeModels(configured map[string][]string) (map[string][]string, error) {
	result := map[string][]string{"omp": {}, "codex": {}}
	for runtimeName, models := range configured {
		name := strings.TrimSpace(runtimeName)
		switch name {
		case "omp", "codex", "claude":
		default:
			return nil, fmt.Errorf("runtimeModels contains unsupported runtime %q", runtimeName)
		}
		if len(models) > 64 {
			return nil, fmt.Errorf("runtimeModels.%s cannot contain more than 64 models", name)
		}
		seen := make(map[string]struct{}, len(models))
		normalized := make([]string, 0, len(models))
		for _, model := range models {
			model = strings.TrimSpace(model)
			if model == "" || len(model) > 128 {
				return nil, fmt.Errorf("runtimeModels.%s entries must be between 1 and 128 characters", name)
			}
			if _, duplicate := seen[model]; duplicate {
				continue
			}
			seen[model] = struct{}{}
			normalized = append(normalized, model)
		}
		result[name] = normalized
	}
	return result, nil
}

func parseDuration(name, value string) (time.Duration, error) {
	if strings.TrimSpace(value) == "" {
		return 0, fmt.Errorf("%s is required", name)
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration: %w", name, err)
	}
	return parsed, nil
}

func expandOptionalPath(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", nil
	}
	return expandHome(value)
}

func expandHome(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "~" || strings.HasPrefix(value, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		if value == "~" {
			return home, nil
		}
		return filepath.Join(home, strings.TrimPrefix(value, "~/")), nil
	}
	if filepath.IsAbs(value) {
		return filepath.Clean(value), nil
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", fmt.Errorf("resolve path %q: %w", value, err)
	}
	return absolute, nil
}

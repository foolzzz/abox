package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
)

type Config struct {
	HomeDirectory          string
	ServerAddress          string
	ServerTLS              bool
	ServerName             string
	HostID                 string
	HostName               string
	EnrollmentToken        string
	StateDirectory         string
	WorkspaceRoots         []string
	OMPBinary              string
	CodexBinary            string
	TmuxBinary             string
	EnableCodex            bool
	ClaudeBinary           string
	EnableClaude           bool
	ClaudePermissionMode   string
	HealthAddress          string
	MaxActiveBoxes         int
	MaxTerminalSessions    int
	JournalMaxBytes        int64
	JournalMaxRecords      int
	JournalMaxRecordBytes  int
	IdempotencyMaxFrames   int
	IdempotencyMaxCommands int
	MaxRunDuration         time.Duration
	HeartbeatInterval      time.Duration
	ShutdownTimeout        time.Duration
	RuntimeProbeTime       time.Duration
	RuntimeStateRetention  time.Duration
	DaemonVersion          string
}

type daemonFileConfig struct {
	ServerAddress          string   `json:"serverAddress"`
	ServerTLS              bool     `json:"serverTLS"`
	ServerName             string   `json:"serverName"`
	HostID                 string   `json:"hostId"`
	HostName               string   `json:"hostName"`
	EnrollmentToken        string   `json:"enrollmentToken"`
	StateDirectory         string   `json:"stateDirectory"`
	WorkspaceRoots         []string `json:"workspaceRoots"`
	OMPBinary              string   `json:"ompBinary"`
	CodexBinary            string   `json:"codexBinary"`
	EnableCodex            *bool    `json:"enableCodex"`
	TmuxBinary             string   `json:"tmuxBinary"`
	ClaudeBinary           string   `json:"claudeBinary"`
	EnableClaude           bool     `json:"enableClaude"`
	ClaudePermissionMode   string   `json:"claudePermissionMode"`
	HealthAddress          string   `json:"healthAddress"`
	MaxActiveBoxes         int      `json:"maxActiveBoxes"`
	MaxTerminalSessions    int      `json:"maxTerminalSessions"`
	JournalMaxBytes        int64    `json:"journalMaxBytes"`
	JournalMaxRecords      int      `json:"journalMaxRecords"`
	JournalMaxRecordBytes  int      `json:"journalMaxRecordBytes"`
	IdempotencyMaxFrames   int      `json:"idempotencyMaxFrames"`
	IdempotencyMaxCommands int      `json:"idempotencyMaxCommands"`
	MaxRunDuration         string   `json:"maxRunDuration"`
	HeartbeatInterval      string   `json:"heartbeatInterval"`
	ShutdownTimeout        string   `json:"shutdownTimeout"`
	RuntimeProbeTime       string   `json:"runtimeProbeTimeout"`
	RuntimeStateRetention  string   `json:"runtimeStateRetention"`
}

func DefaultHomeDirectory() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".agentboxd"), nil
}

func DefaultConfigPath() (string, error) {
	if configured := strings.TrimSpace(os.Getenv("AGENTBOXD_CONFIG")); configured != "" {
		return expandDaemonPath(configured)
	}
	home, err := DefaultHomeDirectory()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "config.json"), nil
}

func LoadConfig(path string) (Config, error) {
	daemonHome, err := DefaultHomeDirectory()
	if err != nil {
		return Config{}, err
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		return Config{}, fmt.Errorf("resolve user home directory: %w", err)
	}
	if strings.TrimSpace(path) == "" {
		path, err = DefaultConfigPath()
	} else {
		path, err = expandDaemonPath(path)
	}
	if err != nil {
		return Config{}, err
	}
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Config{}, fmt.Errorf("agentboxd config %s does not exist; copy configs/agentboxd.example.json and pass --config", path)
		}
		return Config{}, fmt.Errorf("open agentboxd config: %w", err)
	}
	defer func() { _ = file.Close() }()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var raw daemonFileConfig
	if err := decoder.Decode(&raw); err != nil {
		return Config{}, fmt.Errorf("decode agentboxd config %s: %w", path, err)
	}
	info, err := file.Stat()
	if err != nil {
		return Config{}, fmt.Errorf("stat agentboxd config: %w", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return Config{}, fmt.Errorf("agentboxd config %s must not be accessible by group or others", path)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return Config{}, fmt.Errorf("decode agentboxd config %s: trailing content", path)
		}
		return Config{}, fmt.Errorf("decode agentboxd config %s: %w", path, err)
	}
	raw.HostID, err = ensureConfiguredHostID(path, raw.HostID)
	if err != nil {
		return Config{}, err
	}
	stateDirectory := raw.StateDirectory
	if strings.TrimSpace(stateDirectory) == "" {
		stateDirectory = filepath.Join(daemonHome, "state")
	}
	stateDirectory, err = expandDaemonPath(stateDirectory)
	if err != nil {
		return Config{}, err
	}
	rootCapacity := len(raw.WorkspaceRoots)
	if rootCapacity == 0 {
		rootCapacity = 1
	}
	roots := make([]string, 0, rootCapacity)
	if len(raw.WorkspaceRoots) == 0 {
		roots = append(roots, userHome)
	} else {
		for _, root := range raw.WorkspaceRoots {
			expanded, expandErr := expandDaemonPath(root)
			if expandErr != nil {
				return Config{}, fmt.Errorf("workspace root %q: %w", root, expandErr)
			}
			roots = append(roots, expanded)
		}
	}
	maxRunDuration, err := positiveDuration("maxRunDuration", raw.MaxRunDuration)
	if err != nil {
		return Config{}, err
	}
	heartbeat, err := positiveDuration("heartbeatInterval", raw.HeartbeatInterval)
	if err != nil {
		return Config{}, err
	}
	shutdown, err := positiveDuration("shutdownTimeout", raw.ShutdownTimeout)
	if err != nil {
		return Config{}, err
	}
	probe, err := positiveDuration("runtimeProbeTimeout", raw.RuntimeProbeTime)
	if err != nil {
		return Config{}, err
	}
	stateRetention, err := positiveDuration("runtimeStateRetention", raw.RuntimeStateRetention)
	if err != nil {
		return Config{}, err
	}
	enableCodex := true
	if raw.EnableCodex != nil {
		enableCodex = *raw.EnableCodex
	}
	codexBinary := strings.TrimSpace(raw.CodexBinary)
	if codexBinary == "" {
		codexBinary = "codex"
	}
	tmuxBinary := strings.TrimSpace(raw.TmuxBinary)
	if tmuxBinary == "" {
		tmuxBinary = "tmux"
	}
	result := Config{
		HomeDirectory: daemonHome, ServerAddress: raw.ServerAddress, ServerTLS: raw.ServerTLS,
		ServerName: raw.ServerName, HostID: raw.HostID, HostName: strings.TrimSpace(raw.HostName), EnrollmentToken: raw.EnrollmentToken,
		StateDirectory: stateDirectory, WorkspaceRoots: roots, OMPBinary: raw.OMPBinary,
		CodexBinary: codexBinary, EnableCodex: enableCodex, TmuxBinary: tmuxBinary,
		ClaudeBinary: raw.ClaudeBinary, EnableClaude: raw.EnableClaude,
		ClaudePermissionMode: raw.ClaudePermissionMode, HealthAddress: raw.HealthAddress,
		MaxActiveBoxes: raw.MaxActiveBoxes, MaxTerminalSessions: raw.MaxTerminalSessions,
		JournalMaxBytes: raw.JournalMaxBytes, JournalMaxRecords: raw.JournalMaxRecords,
		JournalMaxRecordBytes: raw.JournalMaxRecordBytes, IdempotencyMaxFrames: raw.IdempotencyMaxFrames,
		IdempotencyMaxCommands: raw.IdempotencyMaxCommands, MaxRunDuration: maxRunDuration,
		HeartbeatInterval: heartbeat, ShutdownTimeout: shutdown, RuntimeProbeTime: probe,
		RuntimeStateRetention: stateRetention,
	}
	if err := result.Validate(); err != nil {
		return Config{}, fmt.Errorf("validate agentboxd config %s: %w", path, err)
	}
	return result, nil
}

func ensureConfiguredHostID(configPath, configured string) (string, error) {
	configured = strings.TrimSpace(configured)
	if configured != "" {
		if _, err := uuid.Parse(configured); err != nil {
			return "", fmt.Errorf("hostId must be a UUID: %w", err)
		}
		return configured, nil
	}
	hostID := uuid.NewString()
	content, err := os.ReadFile(configPath)
	if err != nil {
		return "", fmt.Errorf("read agentboxd config for hostId: %w", err)
	}
	var object map[string]any
	if err := json.Unmarshal(content, &object); err != nil {
		return "", fmt.Errorf("decode agentboxd config for hostId: %w", err)
	}
	object["hostId"] = hostID
	encoded, err := json.MarshalIndent(object, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode agentboxd config with hostId: %w", err)
	}
	if err := writeAtomic(configPath, append(encoded, '\n'), 0o600); err != nil {
		return "", fmt.Errorf("persist generated hostId: %w", err)
	}
	return hostID, nil
}

func (c Config) Validate() error {
	for name, value := range map[string]string{
		"serverAddress": c.ServerAddress, "stateDirectory": c.StateDirectory,
		"ompBinary": c.OMPBinary, "healthAddress": c.HealthAddress,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	if strings.TrimSpace(c.EnrollmentToken) == "" && strings.TrimSpace(c.HostID) == "" {
		return errors.New("enrollmentToken is required until the host has enrolled")
	}
	if len(c.WorkspaceRoots) == 0 {
		return errors.New("at least one workspace root is required")
	}
	if strings.TrimSpace(c.HostID) == "" && c.EnrollmentToken == "replace-me" {
		return errors.New("replace placeholder enrollmentToken before first daemon start")
	}
	if c.EnableCodex && strings.TrimSpace(c.CodexBinary) == "" {
		return errors.New("codexBinary is required when enableCodex is true")
	}
	if strings.TrimSpace(c.TmuxBinary) == "" {
		return errors.New("tmuxBinary is required")
	}
	if c.EnableClaude && strings.TrimSpace(c.ClaudeBinary) == "" {
		return errors.New("claudeBinary is required when enableClaude is true")
	}
	if c.EnableClaude && strings.TrimSpace(c.ClaudePermissionMode) == "" {
		return errors.New("claudePermissionMode is required when enableClaude is true")
	}
	if c.MaxActiveBoxes <= 0 || c.MaxActiveBoxes > 256 {
		return errors.New("maxActiveBoxes must be between 1 and 256")
	}
	if c.MaxTerminalSessions <= 0 || c.MaxTerminalSessions > 256 {
		return errors.New("maxTerminalSessions must be between 1 and 256")
	}
	if c.JournalMaxBytes < 1<<20 || c.JournalMaxBytes > 4<<30 {
		return errors.New("journalMaxBytes must be between 1 MiB and 4 GiB")
	}
	if c.JournalMaxRecords < 100 || c.JournalMaxRecords > 1_000_000 || c.JournalMaxRecordBytes < 1<<10 || c.JournalMaxRecordBytes > 16<<20 {
		return errors.New("journal record limits are outside supported bounds")
	}
	if c.IdempotencyMaxFrames < 100 || c.IdempotencyMaxFrames > 1_000_000 || c.IdempotencyMaxCommands < 100 || c.IdempotencyMaxCommands > 1_000_000 {
		return errors.New("idempotency limits must be between 100 and 1000000")
	}
	if c.HeartbeatInterval < 100*time.Millisecond || c.HeartbeatInterval > 24*time.Hour {
		return errors.New("heartbeatInterval must be between 100ms and 24h")
	}
	if err := validateLoopbackAddress(c.HealthAddress); err != nil {
		return fmt.Errorf("healthAddress: %w", err)
	}
	return nil
}

func positiveDuration(name, value string) (time.Duration, error) {
	parsed, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration: %w", name, err)
	}
	return parsed, nil
}

func validateLoopbackAddress(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid listen address: %w", err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("health endpoint must bind to an explicit loopback address")
	}
	return nil
}

func expandDaemonPath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "~" || strings.HasPrefix(value, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
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
		return "", err
	}
	return absolute, nil
}

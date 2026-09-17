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
)

type Config struct {
	HomeDirectory        string
	ServerAddress        string
	ServerTLS            bool
	ServerName           string
	HostID               string
	EnrollmentToken      string
	StateDirectory       string
	WorkspaceRoots       []string
	OMPBinary            string
	ClaudeBinary         string
	EnableClaude         bool
	ClaudePermissionMode string
	HealthAddress        string
	MaxActiveBoxes       int
	MaxRunDuration       time.Duration
	HeartbeatInterval    time.Duration
	ShutdownTimeout      time.Duration
	RuntimeProbeTime     time.Duration
	DaemonVersion        string
}

type daemonFileConfig struct {
	ServerAddress        string   `json:"serverAddress"`
	ServerTLS            bool     `json:"serverTLS"`
	ServerName           string   `json:"serverName"`
	HostID               string   `json:"hostId"`
	EnrollmentToken      string   `json:"enrollmentToken"`
	StateDirectory       string   `json:"stateDirectory"`
	WorkspaceRoots       []string `json:"workspaceRoots"`
	OMPBinary            string   `json:"ompBinary"`
	ClaudeBinary         string   `json:"claudeBinary"`
	EnableClaude         bool     `json:"enableClaude"`
	ClaudePermissionMode string   `json:"claudePermissionMode"`
	HealthAddress        string   `json:"healthAddress"`
	MaxActiveBoxes       int      `json:"maxActiveBoxes"`
	MaxRunDuration       string   `json:"maxRunDuration"`
	HeartbeatInterval    string   `json:"heartbeatInterval"`
	ShutdownTimeout      string   `json:"shutdownTimeout"`
	RuntimeProbeTime     string   `json:"runtimeProbeTimeout"`
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
	home, err := DefaultHomeDirectory()
	if err != nil {
		return Config{}, err
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
	defer file.Close()
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
	stateDirectory := raw.StateDirectory
	if strings.TrimSpace(stateDirectory) == "" {
		stateDirectory = filepath.Join(home, "state")
	}
	stateDirectory, err = expandDaemonPath(stateDirectory)
	if err != nil {
		return Config{}, err
	}
	roots := make([]string, 0, len(raw.WorkspaceRoots))
	for _, root := range raw.WorkspaceRoots {
		expanded, expandErr := expandDaemonPath(root)
		if expandErr != nil {
			return Config{}, fmt.Errorf("workspace root %q: %w", root, expandErr)
		}
		roots = append(roots, expanded)
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
	result := Config{
		HomeDirectory: home, ServerAddress: raw.ServerAddress, ServerTLS: raw.ServerTLS,
		ServerName: raw.ServerName, HostID: raw.HostID, EnrollmentToken: raw.EnrollmentToken,
		StateDirectory: stateDirectory, WorkspaceRoots: roots, OMPBinary: raw.OMPBinary,
		ClaudeBinary: raw.ClaudeBinary, EnableClaude: raw.EnableClaude,
		ClaudePermissionMode: raw.ClaudePermissionMode, HealthAddress: raw.HealthAddress,
		MaxActiveBoxes: raw.MaxActiveBoxes, MaxRunDuration: maxRunDuration,
		HeartbeatInterval: heartbeat, ShutdownTimeout: shutdown, RuntimeProbeTime: probe,
	}
	if err := result.Validate(); err != nil {
		return Config{}, fmt.Errorf("validate agentboxd config %s: %w", path, err)
	}
	return result, nil
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
	if c.EnableClaude && strings.TrimSpace(c.ClaudeBinary) == "" {
		return errors.New("claudeBinary is required when enableClaude is true")
	}
	if c.EnableClaude && strings.TrimSpace(c.ClaudePermissionMode) == "" {
		return errors.New("claudePermissionMode is required when enableClaude is true")
	}
	if c.MaxActiveBoxes <= 0 || c.MaxActiveBoxes > 256 {
		return errors.New("maxActiveBoxes must be between 1 and 256")
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

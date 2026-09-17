package daemon

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	defaultServerAddress    = "127.0.0.1:9443"
	defaultHealthAddress    = "127.0.0.1:9091"
	defaultWorkspaceRoot    = "/tmp/agentbox-workspaces"
	defaultMaxActiveBoxes   = 4
	defaultMaxRunDuration   = 2 * time.Hour
	defaultHeartbeat        = 15 * time.Second
	defaultShutdownTimeout  = 15 * time.Second
	defaultRuntimeProbeTime = 10 * time.Second
)

// Config is the complete agentboxd configuration. Environment variables are
// intentionally the only configuration source so startup has one deterministic
// precedence order.
type Config struct {
	ServerAddress        string
	ServerTLS            bool
	ServerName           string
	HostID               string
	EnrollmentToken      string
	StateDirectory       string
	WorkspaceRoots       []string
	OMPBinary            string
	ClaudeBinary         string
	ClaudePermissionMode string
	HealthAddress        string
	MaxActiveBoxes       int
	MaxRunDuration       time.Duration
	HeartbeatInterval    time.Duration
	ShutdownTimeout      time.Duration
	RuntimeProbeTime     time.Duration
	DaemonVersion        string
}

func LoadConfig() (Config, error) {
	stateDirectory, err := defaultStateDirectory()
	if err != nil {
		return Config{}, err
	}

	config := Config{
		ServerAddress:        env("AGENTBOX_SERVER_GRPC", defaultServerAddress),
		HostID:               strings.TrimSpace(os.Getenv("AGENTBOX_HOST_ID")),
		EnrollmentToken:      strings.TrimSpace(os.Getenv("AGENTBOX_ENROLLMENT_TOKEN")),
		StateDirectory:       env("AGENTBOX_DAEMON_STATE_DIR", stateDirectory),
		WorkspaceRoots:       splitPaths(env("AGENTBOX_WORKSPACE_ROOTS", env("AGENTBOX_WORKSPACE_ROOT", defaultWorkspaceRoot))),
		OMPBinary:            env("AGENTBOX_OMP_BINARY", "omp"),
		ClaudeBinary:         env("AGENTBOX_CLAUDE_BINARY", "claude"),
		ClaudePermissionMode: env("AGENTBOX_CLAUDE_PERMISSION_MODE", "dontAsk"),
		HealthAddress:        env("AGENTBOX_HEALTH_ADDR", defaultHealthAddress),
		MaxActiveBoxes:       intEnv("AGENTBOX_MAX_ACTIVE_BOXES", defaultMaxActiveBoxes),
		MaxRunDuration:       durationEnv("AGENTBOX_MAX_RUN_DURATION", defaultMaxRunDuration),
		HeartbeatInterval:    durationEnv("AGENTBOX_HEARTBEAT_INTERVAL", defaultHeartbeat),
		ShutdownTimeout:      durationEnv("AGENTBOX_SHUTDOWN_TIMEOUT", defaultShutdownTimeout),
		RuntimeProbeTime:     durationEnv("AGENTBOX_RUNTIME_PROBE_TIMEOUT", defaultRuntimeProbeTime),
		ServerName:           strings.TrimSpace(os.Getenv("AGENTBOX_SERVER_NAME")),
	}
	config.ServerTLS, err = boolEnv("AGENTBOX_SERVER_TLS", false)
	if err != nil {
		return Config{}, err
	}
	if err := config.Validate(); err != nil {
		return Config{}, err
	}
	return config, nil
}

func (c Config) Validate() error {
	if strings.TrimSpace(c.ServerAddress) == "" {
		return errors.New("AGENTBOX_SERVER_GRPC is required")
	}
	if strings.TrimSpace(c.StateDirectory) == "" {
		return errors.New("AGENTBOX_DAEMON_STATE_DIR is required")
	}
	if len(c.WorkspaceRoots) == 0 {
		return errors.New("at least one workspace root is required")
	}
	if strings.TrimSpace(c.OMPBinary) == "" {
		return errors.New("AGENTBOX_OMP_BINARY is required")
	}
	if strings.TrimSpace(c.ClaudeBinary) == "" {
		return errors.New("AGENTBOX_CLAUDE_BINARY is required")
	}
	if strings.TrimSpace(c.ClaudePermissionMode) == "" {
		return errors.New("AGENTBOX_CLAUDE_PERMISSION_MODE is required")
	}
	if c.MaxActiveBoxes <= 0 || c.MaxActiveBoxes > 256 {
		return errors.New("AGENTBOX_MAX_ACTIVE_BOXES must be between 1 and 256")
	}
	if c.MaxRunDuration <= 0 {
		return errors.New("AGENTBOX_MAX_RUN_DURATION must be positive")
	}
	if c.HeartbeatInterval < 100*time.Millisecond || c.HeartbeatInterval > 24*time.Hour {
		return errors.New("AGENTBOX_HEARTBEAT_INTERVAL must be between 100ms and 24h")
	}
	if c.ShutdownTimeout <= 0 || c.RuntimeProbeTime <= 0 {
		return errors.New("daemon timeouts must be positive")
	}
	if err := validateLoopbackAddress(c.HealthAddress); err != nil {
		return fmt.Errorf("AGENTBOX_HEALTH_ADDR: %w", err)
	}
	return nil
}

func defaultStateDirectory() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".agentboxd", "state"), nil
}

func splitPaths(value string) []string {
	parts := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == '\n' || r == rune(os.PathListSeparator)
	})
	paths := make([]string, 0, len(parts))
	for _, part := range parts {
		if path := strings.TrimSpace(part); path != "" {
			paths = append(paths, path)
		}
	}
	return paths
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

func env(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func intEnv(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0
	}
	return parsed
}

func durationEnv(key string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0
	}
	return parsed
}

func boolEnv(key string, fallback bool) (bool, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s must be a boolean: %w", key, err)
	}
	return parsed, nil
}

package daemon

import (
	hostv1 "agentbox/api"
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

const (
	maxDiscoveredClaudeSessions = 500
	maxClaudeSessionLineBytes   = 1 << 20
)

type runtimeSessionDiscovery struct {
	claudeBinary string
}

func (d runtimeSessionDiscovery) HandleRuntimeSessionQuery(ctx context.Context, query *hostv1.RuntimeSessionQuery) *hostv1.RuntimeSessionList {
	result := &hostv1.RuntimeSessionList{RequestId: query.GetRequestId()}
	if strings.TrimSpace(query.GetAction()) == "stop" {
		reference, err := resolveClaudeBackgroundJobReference(ctx, d.claudeBinary, query.GetSessionRef())
		if err != nil {
			result.Error = err.Error()
			return result
		}
		binary := strings.TrimSpace(d.claudeBinary)
		if binary == "" {
			binary = "claude"
		}
		if output, err := exec.CommandContext(ctx, binary, "stop", reference).CombinedOutput(); err != nil {
			result.Error = strings.TrimSpace(string(output))
			if result.Error == "" {
				result.Error = err.Error()
			}
		}
		return result
	}
	if strings.TrimSpace(query.GetRuntimeType()) != "claude" {
		result.Error = "session discovery is currently supported only for Claude"
		return result
	}
	sessions, err := discoverClaudeSessions(ctx)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	result.Sessions = sessions
	return result
}

type claudeAgentRecord struct {
	ID        string `json:"id"`
	SessionID string `json:"sessionId"`
	Kind      string `json:"kind"`
}

func resolveClaudeBackgroundJobReference(ctx context.Context, claudeBinary, reference string) (string, error) {
	reference = strings.TrimSpace(reference)
	if reference == "" {
		return "", errors.New("session reference is required")
	}
	binary := strings.TrimSpace(claudeBinary)
	if binary == "" {
		binary = "claude"
	}
	output, err := exec.CommandContext(ctx, binary, "agents", "--json").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("list Claude background sessions: %w: %s", err, boundedTerminalOutput(output))
	}
	var agents []claudeAgentRecord
	if err := json.Unmarshal(output, &agents); err != nil {
		return "", fmt.Errorf("decode Claude background sessions: %w", err)
	}
	for _, agent := range agents {
		if !isClaudeBackgroundKind(agent.Kind) || strings.TrimSpace(agent.ID) == "" {
			continue
		}
		if reference == strings.TrimSpace(agent.ID) || reference == strings.TrimSpace(agent.SessionID) {
			return strings.TrimSpace(agent.ID), nil
		}
	}
	return "", fmt.Errorf("Claude session %q is not an attachable background session", reference)
}

func isClaudeBackgroundKind(kind string) bool {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "background", "bg":
		return true
	default:
		return false
	}
}

func discoverClaudeSessions(ctx context.Context) ([]*hostv1.RuntimeSession, error) {
	configDirectory := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR"))
	if configDirectory == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		configDirectory = filepath.Join(home, ".claude")
	}
	live := readLiveClaudeSessions(filepath.Join(configDirectory, "sessions"))
	projectsDirectory := filepath.Join(configDirectory, "projects")
	result := make([]*hostv1.RuntimeSession, 0)
	seen := make(map[string]struct{})
	err := filepath.WalkDir(projectsDirectory, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, os.ErrNotExist) {
				return nil
			}
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || filepath.Ext(path) != ".jsonl" {
			return nil
		}
		reference := strings.TrimSuffix(filepath.Base(path), ".jsonl")
		if _, err := uuid.Parse(reference); err != nil {
			return nil
		}
		workspace, sessionRef := readClaudeSessionHeader(path)
		if sessionRef != "" {
			reference = sessionRef
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		session := &hostv1.RuntimeSession{
			SessionRef:        reference,
			RuntimeType:       "claude",
			Workspace:         workspace,
			Name:              filepath.Base(workspace),
			Status:            "stopped",
			UpdatedUnixMillis: info.ModTime().UnixMilli(),
		}
		if metadata, ok := live[reference]; ok {
			session.Running = metadata.Running
			session.Status = metadata.Status
			if metadata.CWD != "" {
				session.Workspace = metadata.CWD
			}
			if metadata.Name != "" {
				session.Name = metadata.Name
			}
			if metadata.UpdatedAt > 0 {
				session.UpdatedUnixMillis = metadata.UpdatedAt
			}
		}
		if session.Name == "" {
			session.Name = reference[:8]
		}
		seen[reference] = struct{}{}
		result = append(result, session)
		return nil
	})
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	for reference, metadata := range live {
		if _, ok := seen[reference]; ok || !metadata.Running {
			continue
		}
		name := metadata.Name
		if name == "" {
			name = reference[:8]
		}
		result = append(result, &hostv1.RuntimeSession{
			SessionRef: reference, RuntimeType: "claude", Workspace: metadata.CWD,
			Name: name, Status: metadata.Status, Running: true, UpdatedUnixMillis: metadata.UpdatedAt,
		})
	}
	sort.Slice(result, func(left, right int) bool {
		return result[left].UpdatedUnixMillis > result[right].UpdatedUnixMillis
	})
	if len(result) > maxDiscoveredClaudeSessions {
		result = result[:maxDiscoveredClaudeSessions]
	}
	return result, nil
}

func readClaudeSessionHeader(path string) (string, string) {
	file, err := os.Open(path)
	if err != nil {
		return "", ""
	}
	defer func() { _ = file.Close() }()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), maxClaudeSessionLineBytes)
	for line := 0; line < 128 && scanner.Scan(); line++ {
		var metadata struct {
			SessionID       string `json:"sessionId"`
			LegacySessionID string `json:"session_id"`
			CWD             string `json:"cwd"`
		}
		if json.Unmarshal(scanner.Bytes(), &metadata) != nil {
			continue
		}
		if metadata.SessionID == "" {
			metadata.SessionID = metadata.LegacySessionID
		}
		if metadata.CWD != "" {
			return metadata.CWD, metadata.SessionID
		}
	}
	return "", ""
}

type liveClaudeSession struct {
	CWD       string
	Name      string
	Status    string
	UpdatedAt int64
	Running   bool
	Kind      string
	JobID     string
}

func readLiveClaudeSessions(directory string) map[string]liveClaudeSession {
	result := make(map[string]liveClaudeSession)
	entries, err := os.ReadDir(directory)
	if err != nil {
		return result
	}
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		content, err := os.ReadFile(filepath.Join(directory, entry.Name()))
		if err != nil || len(content) > maxClaudeSessionLineBytes {
			continue
		}
		var metadata struct {
			PID       int    `json:"pid"`
			SessionID string `json:"sessionId"`
			CWD       string `json:"cwd"`
			Name      string `json:"name"`
			Status    string `json:"status"`
			UpdatedAt int64  `json:"updatedAt"`
			Kind      string `json:"kind"`
			JobID     string `json:"jobId"`
		}
		if json.Unmarshal(content, &metadata) != nil || metadata.SessionID == "" {
			continue
		}
		processRunning := metadata.PID > 0 && syscall.Kill(metadata.PID, 0) == nil
		attachable := processRunning && isClaudeBackgroundKind(metadata.Kind) && strings.TrimSpace(metadata.JobID) != ""
		status := metadata.Status
		if processRunning && !attachable {
			status = "interactive"
		} else if status == "" {
			status = "running"
		}
		if !processRunning {
			status = "stopped"
		}
		result[metadata.SessionID] = liveClaudeSession{CWD: metadata.CWD, Name: metadata.Name, Status: status, UpdatedAt: metadata.UpdatedAt, Running: attachable, Kind: metadata.Kind, JobID: metadata.JobID}
	}
	return result
}

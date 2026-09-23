package daemon

import (
	hostv1 "agentbox/api"
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

const (
	maxDiscoveredClaudeSessions = 500
	maxClaudeSessionLineBytes   = 1 << 20
)

type runtimeSessionDiscovery struct{}

func (runtimeSessionDiscovery) HandleRuntimeSessionQuery(ctx context.Context, query *hostv1.RuntimeSessionQuery) *hostv1.RuntimeSessionList {
	result := &hostv1.RuntimeSessionList{RequestId: query.GetRequestId()}
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
		result = append(result, session)
		return nil
	})
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
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
		}
		if json.Unmarshal(content, &metadata) != nil || metadata.SessionID == "" {
			continue
		}
		running := metadata.PID > 0 && syscall.Kill(metadata.PID, 0) == nil
		status := metadata.Status
		if status == "" {
			status = "running"
		}
		if !running {
			status = "stopped"
		}
		result[metadata.SessionID] = liveClaudeSession{CWD: metadata.CWD, Name: metadata.Name, Status: status, UpdatedAt: metadata.UpdatedAt, Running: running}
	}
	return result
}

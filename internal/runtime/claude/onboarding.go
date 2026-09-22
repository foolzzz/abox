package claude

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const maxClaudeConfigBytes = 16 << 20

type authStatus struct {
	LoggedIn        bool   `json:"loggedIn"`
	ConfigDirectory string `json:"configDirectory"`
}

func claudeAuthenticated(status authStatus, apiKey string) bool {
	return status.LoggedIn || strings.TrimSpace(apiKey) != ""
}

func completeClaudeOnboarding(configDirectory, version string) (bool, error) {
	configDirectory = filepath.Clean(strings.TrimSpace(configDirectory))
	if configDirectory == "." || configDirectory == string(filepath.Separator) {
		return false, errors.New("claude config directory is required")
	}
	configPath := configDirectory + ".json"
	var document map[string]json.RawMessage
	original, err := readClaudeConfig(configPath)
	if errors.Is(err, os.ErrNotExist) {
		document = make(map[string]json.RawMessage)
		original = nil
	} else if err != nil {
		return false, err
	} else if err := json.Unmarshal(original, &document); err != nil {
		return false, fmt.Errorf("decode Claude config: %w", err)
	}
	if document == nil {
		document = make(map[string]json.RawMessage)
	}
	var completed bool
	if raw := document["hasCompletedOnboarding"]; len(raw) > 0 && json.Unmarshal(raw, &completed) == nil && completed {
		return false, nil
	}
	document["hasCompletedOnboarding"] = json.RawMessage("true")
	if version = firstVersionToken(version); version != "" {
		encodedVersion, _ := json.Marshal(version)
		document["lastOnboardingVersion"] = encodedVersion
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		return false, fmt.Errorf("encode Claude config: %w", err)
	}
	if len(encoded) > maxClaudeConfigBytes {
		return false, errors.New("Claude config exceeds size limit")
	}
	encoded = append(encoded, '\n')
	if len(original) > 0 {
		backupDirectory := filepath.Join(configDirectory, "backups")
		if err := os.MkdirAll(backupDirectory, 0o700); err != nil {
			return false, fmt.Errorf("create Claude config backup directory: %w", err)
		}
		backupPath := filepath.Join(backupDirectory, fmt.Sprintf(".claude.json.agentbox-backup.%d", time.Now().UnixMilli()))
		if err := os.WriteFile(backupPath, original, 0o600); err != nil {
			return false, fmt.Errorf("backup Claude config: %w", err)
		}
	}
	if err := writeClaudeConfigAtomic(configPath, encoded); err != nil {
		return false, err
	}
	return true, nil
}

func readClaudeConfig(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("Claude config must not be a symbolic link")
	}
	if info.Size() > maxClaudeConfigBytes {
		return nil, errors.New("Claude config exceeds size limit")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	content, err := io.ReadAll(io.LimitReader(file, maxClaudeConfigBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read Claude config: %w", err)
	}
	if len(content) > maxClaudeConfigBytes {
		return nil, errors.New("Claude config exceeds size limit")
	}
	return content, nil
}

func writeClaudeConfigAtomic(path string, content []byte) (returnErr error) {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create Claude config directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".claude.json.agentbox-*")
	if err != nil {
		return fmt.Errorf("create temporary Claude config: %w", err)
	}
	temporaryPath := temporary.Name()
	closed := false
	defer func() {
		if !closed {
			_ = temporary.Close()
		}
		if returnErr != nil {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return fmt.Errorf("secure temporary Claude config: %w", err)
	}
	if _, err := io.Copy(temporary, bytes.NewReader(content)); err != nil {
		return fmt.Errorf("write temporary Claude config: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync temporary Claude config: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary Claude config: %w", err)
	}
	closed = true
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace Claude config: %w", err)
	}
	return nil
}

func firstVersionToken(version string) string {
	fields := strings.Fields(version)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

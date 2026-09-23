package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureTmuxUpdateEnvironmentAddsClaudeAPIKey(t *testing.T) {
	directory := t.TempDir()
	recordPath := filepath.Join(directory, "record")
	tmuxPath := filepath.Join(directory, "tmux")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"show-options\" ]; then echo 'DISPLAY SSH_AUTH_SOCK'; exit 0; fi\n" +
		"if [ \"$1\" = \"set-option\" ]; then printf '%s' \"$4\" > '" + recordPath + "'; exit 0; fi\n" +
		"exit 2\n"
	if err := os.WriteFile(tmuxPath, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	manager := &TerminalManager{tmuxBinary: tmuxPath}
	if err := manager.ensureTmuxUpdateEnvironment(context.Background(), "ANTHROPIC_API_KEY"); err != nil {
		t.Fatalf("configure tmux environment: %v", err)
	}
	content, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(string(content))
	if !containsString(fields, "DISPLAY") || !containsString(fields, "SSH_AUTH_SOCK") || !containsString(fields, "ANTHROPIC_API_KEY") {
		t.Fatalf("update-environment = %q", content)
	}
}

func TestAgentRuntimeCommandResumesClaudeSession(t *testing.T) {
	command, err := agentRuntimeCommand("claude", "claude-opus-4-6", "resume", "3f8f5ddb-c916-421b-8e21-4423a0a96af6")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"claude", "--permission-mode", "bypassPermissions", "--allow-dangerously-skip-permissions", "--resume", "3f8f5ddb-c916-421b-8e21-4423a0a96af6", "--model", "claude-opus-4-6"}
	if strings.Join(command, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("command = %#v, want %#v", command, want)
	}
}

func TestAgentRuntimeCommandRejectsResumeForOtherRuntimes(t *testing.T) {
	if _, err := agentRuntimeCommand("omp", "", "resume", "session"); err == nil {
		t.Fatal("expected non-Claude resume to fail")
	}
}

func TestAgentRuntimeCommandAttachesClaudeBackgroundSession(t *testing.T) {
	command, err := agentRuntimeCommand("claude", "", "attach", "3f8f5ddb-c916-421b-8e21-4423a0a96af6")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"claude", "attach", "3f8f5ddb-c916-421b-8e21-4423a0a96af6"}
	if strings.Join(command, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("command = %#v, want %#v", command, want)
	}
}

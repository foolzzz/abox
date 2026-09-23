package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	hostv1 "agentbox/api"
)

func TestDiscoverClaudeSessionsIncludesStoppedAndRunningSessions(t *testing.T) {
	configDirectory := filepath.Join(t.TempDir(), ".claude")
	projectDirectory := filepath.Join(configDirectory, "projects", "-tmp-project")
	if err := os.MkdirAll(projectDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	stoppedRef := "11111111-1111-4111-8111-111111111111"
	runningRef := "22222222-2222-4222-8222-222222222222"
	for _, reference := range []string{stoppedRef, runningRef} {
		content := fmt.Sprintf("{\"type\":\"mode\",\"sessionId\":%q}\n{\"type\":\"attachment\",\"sessionId\":%q,\"cwd\":\"/tmp/project\"}\n", reference, reference)
		if err := os.WriteFile(filepath.Join(projectDirectory, reference+".jsonl"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	liveDirectory := filepath.Join(configDirectory, "sessions")
	if err := os.MkdirAll(liveDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	live := fmt.Sprintf(`{"pid":%d,"sessionId":%q,"cwd":"/tmp/project","name":"Running Claude","status":"idle","updatedAt":1790130000000}`, os.Getpid(), runningRef)
	if err := os.WriteFile(filepath.Join(liveDirectory, "current.json"), []byte(live), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", configDirectory)

	sessions, err := discoverClaudeSessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 {
		t.Fatalf("sessions = %d, want 2", len(sessions))
	}
	byRef := make(map[string]bool, len(sessions))
	for _, session := range sessions {
		byRef[session.GetSessionRef()] = session.GetRunning()
		if session.GetWorkspace() != "/tmp/project" {
			t.Fatalf("workspace = %q", session.GetWorkspace())
		}
	}
	if byRef[stoppedRef] {
		t.Fatal("stopped session reported running")
	}
	if !byRef[runningRef] {
		t.Fatal("running session reported stopped")
	}
}

func TestRuntimeSessionDiscoveryStopsClaudeSession(t *testing.T) {
	directory := t.TempDir()
	recordPath := filepath.Join(directory, "record")
	binary := filepath.Join(directory, "claude")
	script := "#!/bin/sh\nprintf '%s %s' \"$1\" \"$2\" > '" + recordPath + "'\n"
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	handler := runtimeSessionDiscovery{claudeBinary: binary}
	result := handler.HandleRuntimeSessionQuery(context.Background(), &hostv1.RuntimeSessionQuery{RequestId: "request", RuntimeType: "claude", Action: "stop", SessionRef: "shared-session"})
	if result.GetError() != "" {
		t.Fatalf("stop session: %s", result.GetError())
	}
	content, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "stop shared-session" {
		t.Fatalf("command = %q", content)
	}
}

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
	interactiveRef := "33333333-3333-4333-8333-333333333333"
	for _, reference := range []string{stoppedRef, runningRef, interactiveRef} {
		content := fmt.Sprintf("{\"type\":\"mode\",\"sessionId\":%q}\n{\"type\":\"attachment\",\"sessionId\":%q,\"cwd\":\"/tmp/project\"}\n", reference, reference)
		if err := os.WriteFile(filepath.Join(projectDirectory, reference+".jsonl"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	liveDirectory := filepath.Join(configDirectory, "sessions")
	if err := os.MkdirAll(liveDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	live := fmt.Sprintf(`{"pid":%d,"sessionId":%q,"cwd":"/tmp/project","name":"Running Claude","status":"idle","updatedAt":1790130000000,"kind":"bg","jobId":"job-2222"}`, os.Getpid(), runningRef)
	if err := os.WriteFile(filepath.Join(liveDirectory, "background.json"), []byte(live), 0o600); err != nil {
		t.Fatal(err)
	}
	interactive := fmt.Sprintf(`{"pid":%d,"sessionId":%q,"cwd":"/tmp/project","name":"Interactive Claude","status":"idle","updatedAt":1790130000001,"kind":"interactive"}`, os.Getpid(), interactiveRef)
	if err := os.WriteFile(filepath.Join(liveDirectory, "interactive.json"), []byte(interactive), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", configDirectory)

	sessions, err := discoverClaudeSessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 3 {
		t.Fatalf("sessions = %d, want 3", len(sessions))
	}
	byRef := make(map[string]*hostv1.RuntimeSession, len(sessions))
	for _, session := range sessions {
		byRef[session.GetSessionRef()] = session
		if session.GetWorkspace() != "/tmp/project" {
			t.Fatalf("workspace = %q", session.GetWorkspace())
		}
	}
	if byRef[stoppedRef].GetRunning() {
		t.Fatal("stopped session reported running")
	}
	if !byRef[runningRef].GetRunning() {
		t.Fatal("background session reported stopped")
	}
	if byRef[interactiveRef].GetRunning() || byRef[interactiveRef].GetStatus() != "interactive" {
		t.Fatalf("interactive session = running:%t status:%q", byRef[interactiveRef].GetRunning(), byRef[interactiveRef].GetStatus())
	}
}

func TestRuntimeSessionDiscoveryStopsClaudeSession(t *testing.T) {
	directory := t.TempDir()
	recordPath := filepath.Join(directory, "record")
	binary := filepath.Join(directory, "claude")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"agents\" ]; then printf '[{\"id\":\"job-1234\",\"sessionId\":\"shared-session\",\"kind\":\"background\"}]'; exit 0; fi\n" +
		"printf '%s %s' \"$1\" \"$2\" > '" + recordPath + "'\n"
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
	if string(content) != "stop job-1234" {
		t.Fatalf("command = %q", content)
	}
}

func TestResolveClaudeBackgroundJobRejectsInteractiveSession(t *testing.T) {
	directory := t.TempDir()
	binary := filepath.Join(directory, "claude")
	script := "#!/bin/sh\nprintf '[{\"sessionId\":\"interactive-session\",\"kind\":\"interactive\"}]'\n"
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveClaudeBackgroundJobReference(context.Background(), binary, "interactive-session"); err == nil {
		t.Fatal("expected interactive session attach to fail")
	}
}

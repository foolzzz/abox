package claude

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestClaudeAuthenticated(t *testing.T) {
	if !claudeAuthenticated(authStatus{LoggedIn: true}, "") {
		t.Fatal("OAuth login should authenticate Claude")
	}
	if !claudeAuthenticated(authStatus{}, "sk-ant-test") {
		t.Fatal("ANTHROPIC_API_KEY should authenticate Claude")
	}
	if claudeAuthenticated(authStatus{}, "  ") {
		t.Fatal("empty authentication should not authenticate Claude")
	}
}

func TestCompleteClaudeOnboarding(t *testing.T) {
	root := t.TempDir()
	configDirectory := filepath.Join(root, ".claude")
	if err := os.MkdirAll(configDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := configDirectory + ".json"
	original := []byte(`{"hasCompletedOnboarding":false,"oauthAccount":{"emailAddress":"operator@example.com"},"timestamp":1790100000000}`)
	if err := os.WriteFile(configPath, original, 0o600); err != nil {
		t.Fatal(err)
	}

	changed, err := completeClaudeOnboarding(configDirectory, "2.1.280 (Claude Code)")
	if err != nil {
		t.Fatalf("complete onboarding: %v", err)
	}
	if !changed {
		t.Fatal("expected onboarding config to change")
	}
	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(content, &document); err != nil {
		t.Fatal(err)
	}
	var completed bool
	if err := json.Unmarshal(document["hasCompletedOnboarding"], &completed); err != nil || !completed {
		t.Fatalf("hasCompletedOnboarding = %s, err = %v", document["hasCompletedOnboarding"], err)
	}
	var version string
	if err := json.Unmarshal(document["lastOnboardingVersion"], &version); err != nil || version != "2.1.280" {
		t.Fatalf("lastOnboardingVersion = %q, err = %v", version, err)
	}
	backups, err := filepath.Glob(filepath.Join(configDirectory, "backups", ".claude.json.agentbox-backup.*"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("backups = %v, err = %v", backups, err)
	}

	changed, err = completeClaudeOnboarding(configDirectory, "2.1.280 (Claude Code)")
	if err != nil {
		t.Fatalf("repeat onboarding: %v", err)
	}
	if changed {
		t.Fatal("completed onboarding should be idempotent")
	}
}

func TestProbeAutoCompletesOAuthOnboarding(t *testing.T) {
	configDirectory := filepath.Join(t.TempDir(), ".claude")
	writeClaudeProbe(t, configDirectory, true, false)
	adapter := New(WithBinary(filepath.Join(configDirectory, "claude-test")), WithAutoCompleteOnboarding(true))
	if _, _, err := adapter.Probe(context.Background()); err != nil {
		t.Fatalf("probe OAuth Claude: %v", err)
	}
	assertOnboardingComplete(t, configDirectory)
}

func TestProbeAutoCompletesAPIKeyOnboarding(t *testing.T) {
	configDirectory := filepath.Join(t.TempDir(), ".claude")
	writeClaudeProbe(t, configDirectory, false, true)
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test")
	adapter := New(WithBinary(filepath.Join(configDirectory, "claude-test")), WithAutoCompleteOnboarding(true))
	if _, _, err := adapter.Probe(context.Background()); err != nil {
		t.Fatalf("probe API-key Claude: %v", err)
	}
	assertOnboardingComplete(t, configDirectory)
}

func writeClaudeProbe(t *testing.T, configDirectory string, loggedIn, authExitFailure bool) {
	t.Helper()
	if err := os.MkdirAll(configDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configDirectory+".json", []byte(`{"hasCompletedOnboarding":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	exitCode := "0"
	if authExitFailure {
		exitCode = "1"
	}
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"--version\" ]; then echo '2.1.280 (Claude Code)'; exit 0; fi\n" +
		"if [ \"$1\" = \"auth\" ]; then echo '{\"loggedIn\":" + boolJSON(loggedIn) + ",\"configDirectory\":\"" + configDirectory + "\"}'; exit " + exitCode + "; fi\n" +
		"exit 2\n"
	if err := os.WriteFile(filepath.Join(configDirectory, "claude-test"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
}

func boolJSON(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func assertOnboardingComplete(t *testing.T, configDirectory string) {
	t.Helper()
	content, err := os.ReadFile(configDirectory + ".json")
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Completed bool `json:"hasCompletedOnboarding"`
	}
	if err := json.Unmarshal(content, &document); err != nil {
		t.Fatal(err)
	}
	if !document.Completed {
		t.Fatal("Claude onboarding was not completed")
	}
}

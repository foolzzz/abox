package auth

import (
	"bytes"
	"testing"
)

func TestPasswordHashRoundTrip(t *testing.T) {
	hash, err := HashPassword(DefaultAdminPassword)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	if !CheckPassword(hash, DefaultAdminPassword) {
		t.Fatal("valid password was rejected")
	}
	if CheckPassword(hash, "wrong-password") {
		t.Fatal("invalid password was accepted")
	}
}

func TestNormalizeUsername(t *testing.T) {
	username, err := NormalizeUsername("  Admin.User-1 ")
	if err != nil {
		t.Fatalf("normalize username: %v", err)
	}
	if username != "admin.user-1" {
		t.Fatalf("username = %q, want %q", username, "admin.user-1")
	}
	if _, err := NormalizeUsername("bad user"); err == nil {
		t.Fatal("username containing spaces was accepted")
	}
}

func TestSessionTokensAreRandomAndHashable(t *testing.T) {
	first, firstHash, err := NewSessionToken()
	if err != nil {
		t.Fatalf("generate first token: %v", err)
	}
	second, secondHash, err := NewSessionToken()
	if err != nil {
		t.Fatalf("generate second token: %v", err)
	}
	if first == second || bytes.Equal(firstHash, secondHash) {
		t.Fatal("session tokens were not unique")
	}
	if !bytes.Equal(firstHash, HashSessionToken(first)) {
		t.Fatal("session token hash was not deterministic")
	}
}

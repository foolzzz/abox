package server

import (
	"net/http/httptest"
	"testing"
	"time"
)

func TestLoginFailureThrottle(t *testing.T) {
	server := &Server{loginFailures: make(map[string]loginFailure)}
	now := time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC)
	key := "127.0.0.1\x00admin"
	for attempt := range loginFailureLimit - 1 {
		server.recordLoginFailure(key, now.Add(time.Duration(attempt)*time.Second))
		if retry := server.loginRetryAfter(key, now.Add(time.Duration(attempt)*time.Second)); retry != 0 {
			t.Fatalf("attempt %d was throttled early for %s", attempt+1, retry)
		}
	}
	server.recordLoginFailure(key, now.Add(4*time.Second))
	if retry := server.loginRetryAfter(key, now.Add(5*time.Second)); retry <= 0 {
		t.Fatal("fifth failed login was not throttled")
	}
	server.clearLoginFailures(key)
	if retry := server.loginRetryAfter(key, now.Add(6*time.Second)); retry != 0 {
		t.Fatalf("successful login did not clear throttle: %s", retry)
	}
}

func TestLoginThrottleKeyIgnoresClientPort(t *testing.T) {
	first := httptest.NewRequest("POST", "/api/v1/auth/login", nil)
	first.RemoteAddr = "127.0.0.1:12345"
	second := httptest.NewRequest("POST", "/api/v1/auth/login", nil)
	second.RemoteAddr = "127.0.0.1:54321"

	if firstKey, secondKey := loginThrottleKey(first, "admin"), loginThrottleKey(second, "admin"); firstKey != secondKey {
		t.Fatalf("throttle keys differ by client port: %q != %q", firstKey, secondKey)
	}
}

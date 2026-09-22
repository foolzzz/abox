package daemon

import (
	"context"
	"path/filepath"
	"testing"

	hostclient "agentbox/internal/host"
)

func TestDurableIdempotencyStorePrunesOldestFrames(t *testing.T) {
	path := filepath.Join(t.TempDir(), "idempotency.json")
	limits := hostclient.IdempotencyLimits{MaxFrameIDs: 4, MaxCommands: 4}
	store, err := OpenDurableIdempotencyStore(path, limits)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	for _, frameID := range []string{"frame-a", "frame-b", "frame-c", "frame-d", "frame-e"} {
		if err := store.RememberFrame(context.Background(), frameID); err != nil {
			t.Fatalf("remember %s: %v", frameID, err)
		}
	}

	assertDurableFrameSeen(t, store, "frame-a", false)
	for _, frameID := range []string{"frame-b", "frame-c", "frame-d", "frame-e"} {
		assertDurableFrameSeen(t, store, frameID, true)
	}

	reopened, err := OpenDurableIdempotencyStore(path, limits)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	assertDurableFrameSeen(t, reopened, "frame-a", false)
	assertDurableFrameSeen(t, reopened, "frame-e", true)
}

func assertDurableFrameSeen(t *testing.T, store *DurableIdempotencyStore, frameID string, expected bool) {
	t.Helper()
	seen, err := store.FrameSeen(context.Background(), frameID)
	if err != nil {
		t.Fatalf("check %s: %v", frameID, err)
	}
	if seen != expected {
		t.Fatalf("frame %s seen = %v, want %v", frameID, seen, expected)
	}
}

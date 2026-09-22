package host

import (
	"context"
	"path/filepath"
	"testing"
)

func TestFileIdempotencyStorePrunesOldestFrames(t *testing.T) {
	path := filepath.Join(t.TempDir(), "idempotency.json")
	limits := IdempotencyLimits{MaxFrameIDs: 4, MaxCommands: 4}
	store, err := OpenFileIdempotencyStore(path, limits)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	for _, frameID := range []string{"frame-a", "frame-b", "frame-c", "frame-d", "frame-e"} {
		if err := store.RememberFrame(context.Background(), frameID); err != nil {
			t.Fatalf("remember %s: %v", frameID, err)
		}
	}

	assertFrameSeen(t, store, "frame-a", false)
	for _, frameID := range []string{"frame-b", "frame-c", "frame-d", "frame-e"} {
		assertFrameSeen(t, store, frameID, true)
	}

	reopened, err := OpenFileIdempotencyStore(path, limits)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	assertFrameSeen(t, reopened, "frame-a", false)
	assertFrameSeen(t, reopened, "frame-e", true)
}

func assertFrameSeen(t *testing.T, store IdempotencyStore, frameID string, expected bool) {
	t.Helper()
	seen, err := store.FrameSeen(context.Background(), frameID)
	if err != nil {
		t.Fatalf("check %s: %v", frameID, err)
	}
	if seen != expected {
		t.Fatalf("frame %s seen = %v, want %v", frameID, seen, expected)
	}
}

package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHandleStaticServesRootedFilesAndSPAFallback(t *testing.T) {
	staticDir := t.TempDir()
	writeTestFile(t, filepath.Join(staticDir, "index.html"), "app shell")
	writeTestFile(t, filepath.Join(staticDir, "app.js"), "console.log('ok')")

	root, err := os.OpenRoot(staticDir)
	if err != nil {
		t.Fatalf("open static root: %v", err)
	}
	t.Cleanup(func() { _ = root.Close() })
	server := &Server{staticRoot: root}

	for _, test := range []struct {
		name string
		path string
		want string
	}{
		{name: "asset", path: "/app.js", want: "console.log('ok')"},
		{name: "spa fallback", path: "/boxes/example", want: "app shell"},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			response := httptest.NewRecorder()

			server.handleStatic(response, request)

			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
			}
			if body := response.Body.String(); body != test.want {
				t.Fatalf("body = %q, want %q", body, test.want)
			}
		})
	}
}

func TestHandleStaticRejectsSymlinkEscape(t *testing.T) {
	parentDir := t.TempDir()
	staticDir := filepath.Join(parentDir, "web")
	if err := os.Mkdir(staticDir, 0o700); err != nil {
		t.Fatalf("create static directory: %v", err)
	}
	writeTestFile(t, filepath.Join(parentDir, "secret.txt"), "outside secret")
	if err := os.Symlink("../secret.txt", filepath.Join(staticDir, "leak.txt")); err != nil {
		t.Skipf("create symlink: %v", err)
	}

	root, err := os.OpenRoot(staticDir)
	if err != nil {
		t.Fatalf("open static root: %v", err)
	}
	t.Cleanup(func() { _ = root.Close() })
	server := &Server{staticRoot: root}
	request := httptest.NewRequest(http.MethodGet, "/leak.txt", nil)
	response := httptest.NewRecorder()

	server.handleStatic(response, request)

	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNotFound)
	}
	if strings.Contains(response.Body.String(), "outside secret") {
		t.Fatal("response exposed a file outside the static root")
	}
}

func writeTestFile(t *testing.T, name, content string) {
	t.Helper()
	if err := os.WriteFile(name, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

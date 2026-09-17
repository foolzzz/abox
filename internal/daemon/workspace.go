package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type WorkspaceRoot struct {
	Path     string
	RealPath string
}

type WorkspaceGuard struct {
	roots     []WorkspaceRoot
	statePath string
}

func NewWorkspaceGuard(paths []string, stateDirectory string) (*WorkspaceGuard, error) {
	statePath, err := secureStateDirectory(stateDirectory)
	if err != nil {
		return nil, err
	}
	roots := make([]WorkspaceRoot, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return nil, fmt.Errorf("resolve workspace root %q: %w", path, err)
		}
		realPath, err := filepath.EvalSymlinks(absolute)
		if err != nil {
			return nil, fmt.Errorf("resolve workspace root %q: %w", path, err)
		}
		info, err := os.Stat(realPath)
		if err != nil {
			return nil, fmt.Errorf("stat workspace root %q: %w", path, err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("workspace root %q is not a directory", path)
		}
		realPath = filepath.Clean(realPath)
		if _, duplicate := seen[realPath]; duplicate {
			continue
		}
		seen[realPath] = struct{}{}
		roots = append(roots, WorkspaceRoot{Path: filepath.Clean(absolute), RealPath: realPath})
	}
	if len(roots) == 0 {
		return nil, errors.New("no usable workspace roots configured")
	}
	return &WorkspaceGuard{roots: roots, statePath: statePath}, nil
}

func (g *WorkspaceGuard) Roots() []WorkspaceRoot {
	return append([]WorkspaceRoot(nil), g.roots...)
}

// ResolveWorkspace returns a symlink-free absolute path under a configured
// root. The resolved path, rather than the user supplied spelling, is passed as
// the runtime cwd so a symlink at the requested path cannot redirect OMP.
func (g *WorkspaceGuard) ResolveWorkspace(path string) (string, error) {
	if path == "" {
		return "", errors.New("workspace is required")
	}
	if !filepath.IsAbs(path) {
		return "", errors.New("workspace must be an absolute path")
	}
	realPath, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		return "", fmt.Errorf("resolve workspace: %w", err)
	}
	info, err := os.Stat(realPath)
	if err != nil {
		return "", fmt.Errorf("stat workspace: %w", err)
	}
	if !info.IsDir() {
		return "", errors.New("workspace is not a directory")
	}
	realPath = filepath.Clean(realPath)
	if withinPath(g.statePath, realPath) {
		return "", errors.New("workspace cannot be inside the daemon state directory")
	}
	for _, root := range g.roots {
		if withinPath(root.RealPath, realPath) {
			return realPath, nil
		}
	}
	return "", errors.New("workspace escapes configured roots")
}

func (g *WorkspaceGuard) ResolveWorkspaceFile(workspace, path string) (string, error) {
	if path == "" {
		return "", nil
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(workspace, path)
	}
	realPath, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		return "", fmt.Errorf("resolve workspace file: %w", err)
	}
	info, err := os.Stat(realPath)
	if err != nil {
		return "", fmt.Errorf("stat workspace file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("workspace file is not a regular file")
	}
	if !withinPath(workspace, realPath) {
		return "", errors.New("workspace file escapes the workspace")
	}
	return realPath, nil
}

func secureStateDirectory(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve state directory: %w", err)
	}
	if err := os.MkdirAll(absolute, 0o700); err != nil {
		return "", fmt.Errorf("create state directory: %w", err)
	}
	realPath, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("resolve state directory symlinks: %w", err)
	}
	info, err := os.Stat(realPath)
	if err != nil {
		return "", fmt.Errorf("stat state directory: %w", err)
	}
	if !info.IsDir() {
		return "", errors.New("state path is not a directory")
	}
	if err := os.Chmod(realPath, 0o700); err != nil {
		return "", fmt.Errorf("restrict state directory permissions: %w", err)
	}
	info, err = os.Stat(realPath)
	if err != nil {
		return "", fmt.Errorf("verify state directory permissions: %w", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return "", errors.New("state directory is accessible by group or other users")
	}
	return filepath.Clean(realPath), nil
}

func withinPath(root, candidate string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(candidate))
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}

package daemon

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	hostv1 "agentbox/api"
	hostclient "agentbox/internal/host"
)

const (
	gitDiffTimeout = 30 * time.Second
	maxDiffBytes   = 4 << 20
)

type gitDiffPayload struct {
	Operation        string `json:"operation"`
	Workspace        string `json:"workspace"`
	BaseRef          string `json:"baseRef"`
	HeadRef          string `json:"headRef"`
	IncludePatch     bool   `json:"includePatch"`
	IncludeUntracked bool   `json:"includeUntracked"`
}

type gitDiffFile struct {
	Path      string `json:"path"`
	Status    string `json:"status"`
	OldPath   string `json:"oldPath,omitempty"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Patch     string `json:"patch,omitempty"`
}

type gitDiffResult struct {
	Status      string        `json:"status"`
	BaseRef     string        `json:"baseRef,omitempty"`
	HeadRef     string        `json:"headRef,omitempty"`
	GeneratedAt time.Time     `json:"generatedAt"`
	Files       []gitDiffFile `json:"files"`
}

func (m *Manager) handleGitDiff(ctx context.Context, command *hostv1.HostCommand) hostclient.CommandResult {
	var payload gitDiffPayload
	if err := decodePayload(command.PayloadJson, &payload); err != nil {
		return failedResult("invalid_payload", err)
	}
	workspace, err := m.guard.ResolveWorkspace(payload.Workspace)
	if err != nil {
		return failedResult("workspace_rejected", err)
	}
	if err := validateGitRef(payload.BaseRef); err != nil {
		return failedResult("invalid_payload", fmt.Errorf("baseRef: %w", err))
	}
	if err := validateGitRef(payload.HeadRef); err != nil {
		return failedResult("invalid_payload", fmt.Errorf("headRef: %w", err))
	}
	if payload.BaseRef == "" {
		payload.BaseRef = "HEAD"
	}
	operationContext, cancel := context.WithTimeout(ctx, gitDiffTimeout)
	defer cancel()
	result, err := collectGitDiff(operationContext, workspace, payload)
	if err != nil {
		return failedResult("git_diff_failed", err)
	}
	return completedJSON(result)
}

func validateGitRef(value string) error {
	if value == "" {
		return nil
	}
	if strings.HasPrefix(value, "-") || strings.ContainsAny(value, "\x00\r\n") {
		return errors.New("invalid git revision")
	}
	return nil
}

func collectGitDiff(ctx context.Context, workspace string, payload gitDiffPayload) (gitDiffResult, error) {
	if _, err := runGit(ctx, workspace, "rev-parse", "--is-inside-work-tree"); err != nil {
		return gitDiffResult{}, fmt.Errorf("workspace is not a git repository: %w", err)
	}
	rangeArg := payload.BaseRef
	if payload.HeadRef != "" {
		rangeArg = payload.BaseRef + "..." + payload.HeadRef
	}
	numstat, err := runGit(ctx, workspace, "diff", "--numstat", "--find-renames", rangeArg, "--")
	if err != nil {
		return gitDiffResult{}, err
	}
	patch := ""
	if payload.IncludePatch {
		patch, err = runGit(ctx, workspace, "diff", "--no-color", "--find-renames", "--unified=3", rangeArg, "--")
		if err != nil {
			return gitDiffResult{}, err
		}
		if len(patch) > maxDiffBytes {
			patch = patch[:maxDiffBytes] + "\n... diff truncated ...\n"
		}
	}
	files := parseNumstat(numstat)
	patches := splitPatches(patch)
	for index := range files {
		if value, ok := patches[files[index].Path]; ok {
			files[index].Patch = value
		}
	}
	if payload.IncludeUntracked {
		untracked, listErr := runGit(ctx, workspace, "ls-files", "--others", "--exclude-standard")
		if listErr != nil {
			return gitDiffResult{}, listErr
		}
		for _, relative := range strings.Split(strings.TrimSpace(untracked), "\n") {
			if relative == "" {
				continue
			}
			file := gitDiffFile{Path: relative, Status: "untracked"}
			absolute := filepath.Join(workspace, filepath.FromSlash(relative))
			if data, readErr := os.ReadFile(absolute); readErr == nil && len(data) <= maxDiffBytes {
				file.Additions = bytes.Count(data, []byte("\n"))
				if len(data) > 0 && data[len(data)-1] != '\n' {
					file.Additions++
				}
				if payload.IncludePatch {
					var builder strings.Builder
					builder.WriteString("--- /dev/null\n+++ b/")
					builder.WriteString(relative)
					builder.WriteString("\n")
					scanner := bufio.NewScanner(bytes.NewReader(data))
					for scanner.Scan() {
						builder.WriteString("+")
						builder.WriteString(scanner.Text())
						builder.WriteString("\n")
					}
					file.Patch = builder.String()
				}
			}
			files = append(files, file)
		}
	}
	return gitDiffResult{Status: "completed", BaseRef: payload.BaseRef, HeadRef: payload.HeadRef, GeneratedAt: time.Now().UTC(), Files: files}, nil
}

func runGit(ctx context.Context, workspace string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = workspace
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), message)
	}
	return stdout.String(), nil
}

func parseNumstat(raw string) []gitDiffFile {
	files := make([]gitDiffFile, 0)
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 {
			continue
		}
		path := parts[2]
		oldPath := ""
		status := "modified"
		if strings.Contains(path, " => ") {
			oldPath, path = parseRenamePath(path)
			status = "renamed"
		}
		files = append(files, gitDiffFile{Path: path, OldPath: oldPath, Status: status, Additions: parseCount(parts[0]), Deletions: parseCount(parts[1])})
	}
	return files
}

func parseCount(value string) int {
	count, _ := strconv.Atoi(value)
	return count
}

func parseRenamePath(value string) (string, string) {
	if open := strings.Index(value, "{"); open >= 0 {
		if close := strings.Index(value[open:], "}"); close >= 0 {
			close += open
			inside := value[open+1 : close]
			parts := strings.SplitN(inside, " => ", 2)
			if len(parts) == 2 {
				return value[:open] + parts[0] + value[close+1:], value[:open] + parts[1] + value[close+1:]
			}
		}
	}
	parts := strings.SplitN(value, " => ", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return "", value
}

func splitPatches(raw string) map[string]string {
	result := make(map[string]string)
	if raw == "" {
		return result
	}
	for _, chunk := range strings.Split(raw, "diff --git ") {
		if strings.TrimSpace(chunk) == "" {
			continue
		}
		full := "diff --git " + chunk
		path := ""
		for _, line := range strings.Split(chunk, "\n") {
			if strings.HasPrefix(line, "+++ b/") {
				path = strings.TrimPrefix(line, "+++ b/")
				break
			}
		}
		if path != "" {
			result[path] = full
		}
	}
	return result
}

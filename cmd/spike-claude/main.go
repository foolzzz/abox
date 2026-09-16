package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"agentbox/internal/events"
	runtimeapi "agentbox/internal/runtime"
	runtimeclaude "agentbox/internal/runtime/claude"
)

type options struct {
	binary           string
	workspace        string
	model            string
	systemPromptFile string
	permissionMode   string
	sessionID        string
	firstPrompt      string
	secondPrompt     string
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	var options options
	flag.StringVar(&options.binary, "binary", "claude", "Claude Code binary")
	flag.StringVar(&options.workspace, "workspace", ".", "workspace for the Claude subprocess")
	flag.StringVar(&options.model, "model", "", "optional Claude model")
	flag.StringVar(&options.systemPromptFile, "system-prompt-file", "", "optional append-system-prompt file")
	flag.StringVar(&options.permissionMode, "permission-mode", "dontAsk", "static permission mode: dontAsk, acceptEdits, auto, plan, or bypassPermissions")
	flag.StringVar(&options.sessionID, "resume", "", "Claude session ID to resume")
	flag.StringVar(&options.firstPrompt, "first", "Reply with exactly: first turn complete", "first turn prompt")
	flag.StringVar(&options.secondPrompt, "second", "Reply with exactly: second turn complete", "second turn prompt")
	flag.Parse()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	adapter := runtimeclaude.New(runtimeclaude.WithBinary(options.binary))
	handle, err := adapter.Start(ctx, runtimeapi.StartSpec{
		BoxID:            "spike-claude",
		RunID:            "two-turn",
		Workspace:        options.workspace,
		SessionRef:       options.sessionID,
		Model:            options.model,
		SystemPromptFile: options.systemPromptFile,
		ApprovalMode:     options.permissionMode,
	})
	if err != nil {
		return fmt.Errorf("start Claude adapter: %w", err)
	}

	stopped := false
	defer func() {
		if stopped {
			return
		}
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		_ = adapter.Stop(stopCtx, handle, runtimeapi.StopForce)
	}()

	eventStream := adapter.Events(handle)
	if err := adapter.Send(ctx, handle, runtimeapi.Input{
		ID:      "turn-1",
		RunID:   "spike-run-1",
		Kind:    runtimeapi.InputPrompt,
		Message: options.firstPrompt,
	}); err != nil {
		return fmt.Errorf("send first turn: %w", err)
	}
	if err := printUntilTurnBoundary(ctx, eventStream); err != nil {
		return fmt.Errorf("first turn: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Claude session: %s\n", handle.SessionRef())
	if err := adapter.Send(ctx, handle, runtimeapi.Input{
		ID:      "turn-2",
		RunID:   "spike-run-2",
		Kind:    runtimeapi.InputFollowUp,
		Message: options.secondPrompt,
	}); err != nil {
		return fmt.Errorf("send second turn: %w", err)
	}
	if err := printUntilTurnBoundary(ctx, eventStream); err != nil {
		return fmt.Errorf("second turn: %w", err)
	}

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopCancel()
	if err := adapter.Stop(stopCtx, handle, runtimeapi.StopGraceful); err != nil {
		return fmt.Errorf("stop Claude adapter: %w", err)
	}
	stopped = true
	return nil
}

func printUntilTurnBoundary(ctx context.Context, eventStream <-chan runtimeapi.Event) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetEscapeHTML(false)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case event, open := <-eventStream:
			if !open {
				return errors.New("Claude event stream closed before a turn boundary")
			}
			if err := encoder.Encode(event); err != nil {
				return fmt.Errorf("write event: %w", err)
			}
			switch event.Type {
			case events.RunCompleted:
				return nil
			case events.RunFailed:
				return fmt.Errorf("turn failed: %s", event.Payload)
			}
		}
	}
}

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
	"agentbox/internal/runtime"
	ompruntime "agentbox/internal/runtime/omp"

	"github.com/google/uuid"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "spike-omp:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		workspace = flag.String("workspace", "", "workspace in which to start OMP (or pass it as the sole positional argument)")
		binary    = flag.String("omp", "omp", "OMP executable")
		model     = flag.String("model", "", "optional OMP model")
		approval  = flag.String("approval-mode", "write", "OMP approval mode")
		first     = flag.String("first", "Inspect the workspace and summarize its purpose in one paragraph.", "first prompt")
		second    = flag.String("second", "Now list the two most important implementation risks.", "second prompt")
		timeout   = flag.Duration("timeout", 10*time.Minute, "overall spike timeout")
	)
	flag.Parse()

	if *workspace == "" {
		if flag.NArg() != 1 {
			return errors.New("supply exactly one workspace with -workspace or as a positional argument")
		}
		*workspace = flag.Arg(0)
	} else if flag.NArg() != 0 {
		return errors.New("do not combine -workspace with a positional workspace")
	}
	if *first == "" || *second == "" {
		return errors.New("both prompts must be non-empty")
	}
	if *timeout <= 0 {
		return errors.New("timeout must be positive")
	}

	signalContext, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	ctx, cancel := context.WithTimeout(signalContext, *timeout)
	defer cancel()

	adapter := ompruntime.New(ompruntime.Config{Binary: *binary})
	handle, err := adapter.Start(ctx, runtime.StartSpec{
		BoxID:             uuid.NewString(),
		RunID:             uuid.NewString(),
		Workspace:         *workspace,
		Model:             *model,
		ApprovalMode:      *approval,
		SubagentEventMode: "progress",
	})
	if err != nil {
		return err
	}

	stopped := false
	defer func() {
		if stopped {
			return
		}
		stopContext, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		_ = adapter.Stop(stopContext, handle, runtime.StopForce)
	}()

	encoder := json.NewEncoder(os.Stdout)
	encoder.SetEscapeHTML(false)
	eventStream := adapter.Events(handle)

	prompts := []runtime.Input{
		{ID: "spike-1", RunID: uuid.NewString(), Kind: runtime.InputPrompt, Message: *first},
		{ID: "spike-2", RunID: uuid.NewString(), Kind: runtime.InputPrompt, Message: *second},
	}
	for _, prompt := range prompts {
		if err := adapter.Send(ctx, handle, prompt); err != nil {
			return err
		}
		if err := streamUntilTerminal(ctx, encoder, eventStream); err != nil {
			return err
		}
	}

	stopContext, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
	err = adapter.Stop(stopContext, handle, runtime.StopGraceful)
	stopCancel()
	if err != nil {
		return err
	}
	stopped = true

	for event := range eventStream {
		if err := encoder.Encode(event); err != nil {
			return fmt.Errorf("encode event: %w", err)
		}
	}
	return nil
}

func streamUntilTerminal(ctx context.Context, encoder *json.Encoder, eventsChannel <-chan runtime.Event) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case event, ok := <-eventsChannel:
			if !ok {
				return errors.New("OMP event stream closed before a terminal run event")
			}
			if err := encoder.Encode(event); err != nil {
				return fmt.Errorf("encode event: %w", err)
			}
			if event.Type == events.RunCompleted {
				return nil
			}
			if event.Type == events.RunFailed {
				return errors.New("OMP run failed; see the preceding normalized event")
			}
		}
	}
}

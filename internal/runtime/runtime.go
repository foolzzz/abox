package runtime

import (
	"context"
	"encoding/json"
	"time"
)

type Type string

const (
	TypeOMP    Type = "omp"
	TypeCodex  Type = "codex"
	TypeClaude Type = "claude"
	TypeACP    Type = "acp"
)

type Capabilities struct {
	Steer               bool `json:"steer"`
	FollowUp            bool `json:"followUp"`
	SubagentEvents      bool `json:"subagentEvents"`
	TodoEvents          bool `json:"todoEvents"`
	Resume              bool `json:"resume"`
	HostTools           bool `json:"hostTools"`
	InteractiveApproval bool `json:"interactiveApproval"`
}

type StartSpec struct {
	BoxID             string
	RunID             string
	Workspace         string
	SessionRef        string
	Model             string
	SystemPromptFile  string
	ApprovalMode      string
	Environment       map[string]string
	SubagentEventMode string
}

type InputKind string

const (
	InputPrompt           InputKind = "prompt"
	InputSteer            InputKind = "steer"
	InputFollowUp         InputKind = "follow_up"
	InputApprovalResponse InputKind = "approval_response"
)

type Input struct {
	ID         string
	RunID      string
	Kind       InputKind
	Message    string
	ApprovalID string
	Approved   bool
	Payload    json.RawMessage
}

type StopMode string

const (
	StopGraceful StopMode = "graceful"
	StopForce    StopMode = "force"
)

type Handle interface {
	ID() string
	SessionRef() string
}

type Event struct {
	ID                string          `json:"eventId"`
	Type              string          `json:"type"`
	BoxID             string          `json:"boxId"`
	RunID             string          `json:"runId,omitempty"`
	RuntimeInstanceID string          `json:"runtimeInstanceId,omitempty"`
	RuntimeSeq        uint64          `json:"runtimeSeq"`
	ActorKind         string          `json:"actorKind"`
	ActorID           string          `json:"actorId,omitempty"`
	OccurredAt        time.Time       `json:"occurredAt"`
	Payload           json.RawMessage `json:"payload"`
}

type State struct {
	Status       string
	SessionRef   string
	Capabilities Capabilities
	StartedAt    time.Time
	LastEventAt  time.Time
}

type Adapter interface {
	Name() Type
	Probe(ctx context.Context) (version string, capabilities Capabilities, err error)
	Start(ctx context.Context, spec StartSpec) (Handle, error)
	Send(ctx context.Context, handle Handle, input Input) error
	Interrupt(ctx context.Context, handle Handle) error
	Stop(ctx context.Context, handle Handle, mode StopMode) error
	Events(handle Handle) <-chan Event
	Inspect(ctx context.Context, handle Handle) (State, error)
}

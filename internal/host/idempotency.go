package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	hostv1 "agentbox/api"
)

type CommandRecord struct {
	Key          string              `json:"key"`
	CommandID    string              `json:"commandId"`
	Stage        hostv1.CommandStage `json:"stage"`
	ErrorCode    string              `json:"errorCode,omitempty"`
	ErrorMessage string              `json:"errorMessage,omitempty"`
	ResultJSON   []byte              `json:"resultJson,omitempty"`
	UpdatedAt    time.Time           `json:"updatedAt"`
}

type IdempotencyStore interface {
	FrameSeen(ctx context.Context, frameID string) (bool, error)
	RememberFrame(ctx context.Context, frameID string) error
	BeginCommand(ctx context.Context, key, commandID string) (record CommandRecord, firstDelivery bool, err error)
	StartCommand(ctx context.Context, key, commandID string) (record CommandRecord, start bool, err error)
	SetCommandResult(ctx context.Context, key string, result CommandRecord) error
	AbandonCommand(ctx context.Context, key, commandID string) error
}

type IdempotencyLimits struct {
	MaxFrameIDs int
	MaxCommands int
}

func DefaultIdempotencyLimits() IdempotencyLimits {
	return IdempotencyLimits{MaxFrameIDs: 16_384, MaxCommands: 8_192}
}

type idempotencyState struct {
	NextOrder uint64                   `json:"nextOrder"`
	Frames    map[string]uint64        `json:"frames"`
	Commands  map[string]CommandRecord `json:"commands"`
}

type FileIdempotencyStore struct {
	mu     sync.Mutex
	path   string
	limits IdempotencyLimits
	state  idempotencyState
}

func OpenFileIdempotencyStore(path string, limits IdempotencyLimits) (*FileIdempotencyStore, error) {
	if path == "" {
		return nil, protocolError("open idempotency store", CodeConfiguration, errors.New("idempotency path is required"))
	}
	if limits.MaxFrameIDs <= 0 || limits.MaxCommands <= 0 {
		return nil, protocolError("open idempotency store", CodeConfiguration, errors.New("idempotency limits must be positive"))
	}
	store := &FileIdempotencyStore{
		path:   path,
		limits: limits,
		state: idempotencyState{
			Frames:   make(map[string]uint64),
			Commands: make(map[string]CommandRecord),
		},
	}
	if err := readBoundedJSON(path, maxStateFileBytes, &store.state); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, protocolError("load idempotency store", CodePersistence, err)
		}
	}
	if store.state.Frames == nil {
		store.state.Frames = make(map[string]uint64)
	}
	if store.state.Commands == nil {
		store.state.Commands = make(map[string]CommandRecord)
	}
	if len(store.state.Frames) > limits.MaxFrameIDs || len(store.state.Commands) > limits.MaxCommands {
		return nil, protocolError("load idempotency store", CodeIdempotency, ErrStoreFull)
	}
	return store, nil
}

func (s *FileIdempotencyStore) FrameSeen(ctx context.Context, frameID string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if frameID == "" {
		return false, protocolError("check frame", CodeProtocol, errors.New("server frame id is required"))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, exists := s.state.Frames[frameID]
	return exists, nil
}

func (s *FileIdempotencyStore) RememberFrame(ctx context.Context, frameID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if frameID == "" {
		return protocolError("remember frame", CodeProtocol, errors.New("server frame id is required"))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.state.Frames[frameID]; exists {
		return nil
	}
	if len(s.state.Frames) >= s.limits.MaxFrameIDs {
		return protocolError("remember frame", CodeIdempotency, ErrStoreFull)
	}
	previousOrder := s.state.NextOrder
	s.state.NextOrder++
	s.state.Frames[frameID] = s.state.NextOrder
	if err := s.saveLocked(); err != nil {
		delete(s.state.Frames, frameID)
		s.state.NextOrder = previousOrder
		return err
	}
	return nil
}

func (s *FileIdempotencyStore) BeginCommand(ctx context.Context, key, commandID string) (CommandRecord, bool, error) {
	if err := ctx.Err(); err != nil {
		return CommandRecord{}, false, err
	}
	if key == "" || commandID == "" {
		return CommandRecord{}, false, protocolError("begin command", CodeProtocol, errors.New("command key and id are required"))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, exists := s.state.Commands[key]; exists {
		if existing.CommandID != commandID {
			return CommandRecord{}, false, protocolError("begin command", CodeIdempotency, fmt.Errorf("idempotency key %q is already bound to command %q", key, existing.CommandID))
		}
		return cloneCommandRecord(existing), false, nil
	}
	for _, existing := range s.state.Commands {
		if existing.CommandID == commandID {
			return cloneCommandRecord(existing), false, nil
		}
	}
	if len(s.state.Commands) >= s.limits.MaxCommands {
		return CommandRecord{}, false, protocolError("begin command", CodeIdempotency, ErrStoreFull)
	}
	record := CommandRecord{
		Key:       key,
		CommandID: commandID,
		Stage:     hostv1.CommandStage_COMMAND_STAGE_ACCEPTED,
		UpdatedAt: time.Now().UTC(),
	}
	s.state.Commands[key] = record
	if err := s.saveLocked(); err != nil {
		delete(s.state.Commands, key)
		return CommandRecord{}, false, err
	}
	return cloneCommandRecord(record), true, nil
}

func (s *FileIdempotencyStore) StartCommand(ctx context.Context, key, commandID string) (CommandRecord, bool, error) {
	if err := ctx.Err(); err != nil {
		return CommandRecord{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, exists := s.state.Commands[key]
	if !exists {
		return CommandRecord{}, false, protocolError("start command", CodeIdempotency, fmt.Errorf("unknown command key %q", key))
	}
	if current.CommandID != commandID {
		return CommandRecord{}, false, protocolError("start command", CodeIdempotency, errors.New("command id changed"))
	}
	if current.Stage != hostv1.CommandStage_COMMAND_STAGE_ACCEPTED {
		return cloneCommandRecord(current), false, nil
	}
	started := current
	started.Stage = hostv1.CommandStage_COMMAND_STAGE_STARTED
	started.UpdatedAt = time.Now().UTC()
	s.state.Commands[key] = started
	if err := s.saveLocked(); err != nil {
		s.state.Commands[key] = current
		return CommandRecord{}, false, err
	}
	return cloneCommandRecord(started), true, nil
}

func (s *FileIdempotencyStore) SetCommandResult(ctx context.Context, key string, result CommandRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, exists := s.state.Commands[key]
	if !exists {
		return protocolError("update command", CodeIdempotency, fmt.Errorf("unknown command key %q", key))
	}
	if result.CommandID != "" && current.CommandID != result.CommandID {
		return protocolError("update command", CodeIdempotency, errors.New("command id changed"))
	}
	if !validStageTransition(current.Stage, result.Stage) {
		return protocolError("update command", CodeIdempotency, fmt.Errorf("invalid command stage transition %s -> %s", current.Stage, result.Stage))
	}
	result.Key = current.Key
	result.CommandID = current.CommandID
	result.UpdatedAt = time.Now().UTC()
	result.ResultJSON = append([]byte(nil), result.ResultJSON...)
	s.state.Commands[key] = result
	if err := s.saveLocked(); err != nil {
		s.state.Commands[key] = current
		return err
	}
	return nil
}

func (s *FileIdempotencyStore) AbandonCommand(ctx context.Context, key, commandID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, exists := s.state.Commands[key]
	if !exists {
		return nil
	}
	if current.CommandID != commandID || current.Stage != hostv1.CommandStage_COMMAND_STAGE_ACCEPTED {
		return protocolError("abandon command", CodeIdempotency, errors.New("only an unstarted matching command may be abandoned"))
	}
	delete(s.state.Commands, key)
	if err := s.saveLocked(); err != nil {
		s.state.Commands[key] = current
		return err
	}
	return nil
}

func (s *FileIdempotencyStore) saveLocked() error {
	encoded, err := json.Marshal(s.state)
	if err != nil {
		return protocolError("encode idempotency state", CodePersistence, err)
	}
	if len(encoded) > maxStateFileBytes {
		return protocolError("save idempotency state", CodeIdempotency, ErrStoreFull)
	}
	if err := writeFileAtomic(s.path, append(encoded, '\n'), 0o600); err != nil {
		return protocolError("save idempotency state", CodePersistence, err)
	}
	return nil
}

func validStageTransition(from, to hostv1.CommandStage) bool {
	if from == to {
		return true
	}
	if isTerminalStage(from) {
		return false
	}
	switch from {
	case hostv1.CommandStage_COMMAND_STAGE_ACCEPTED:
		return to == hostv1.CommandStage_COMMAND_STAGE_STARTED || isTerminalStage(to)
	case hostv1.CommandStage_COMMAND_STAGE_STARTED:
		return isTerminalStage(to)
	default:
		return false
	}
}

func isTerminalStage(stage hostv1.CommandStage) bool {
	return stage == hostv1.CommandStage_COMMAND_STAGE_COMPLETED ||
		stage == hostv1.CommandStage_COMMAND_STAGE_FAILED ||
		stage == hostv1.CommandStage_COMMAND_STAGE_CANCELLED
}

func cloneCommandRecord(record CommandRecord) CommandRecord {
	record.ResultJSON = append([]byte(nil), record.ResultJSON...)
	return record
}

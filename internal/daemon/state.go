package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	hostv1 "agentbox/api"
	hostclient "agentbox/internal/host"
	runtimeapi "agentbox/internal/runtime"
	"github.com/google/uuid"
)

const (
	stateVersion      = 1
	maxStateFileBytes = 16 << 20
)

type PersistedRuntime struct {
	BoxID             string                  `json:"boxId"`
	RuntimeInstanceID string                  `json:"runtimeInstanceId"`
	RuntimeType       string                  `json:"runtimeType"`
	RuntimeVersion    string                  `json:"runtimeVersion,omitempty"`
	Status            string                  `json:"status"`
	ProcessID         int64                   `json:"processId,omitempty"`
	SessionRef        string                  `json:"sessionRef,omitempty"`
	Capabilities      runtimeapi.Capabilities `json:"capabilities"`
	Workspace         string                  `json:"workspace,omitempty"`
	RunID             string                  `json:"runId,omitempty"`
	StartedAt         time.Time               `json:"startedAt,omitempty"`
	LastEventAt       time.Time               `json:"lastEventAt,omitempty"`
	UpdatedAt         time.Time               `json:"updatedAt"`
}

type runtimeStateFile struct {
	Version int                         `json:"version"`
	Boxes   map[string]PersistedRuntime `json:"boxes"`
}

type StateStore struct {
	mu    sync.Mutex
	path  string
	state runtimeStateFile
}

func OpenStateStore(path string, retention time.Duration) (*StateStore, error) {
	if retention <= 0 {
		return nil, errors.New("runtime state retention must be positive")
	}
	store := &StateStore{
		path: path,
		state: runtimeStateFile{
			Version: stateVersion,
			Boxes:   make(map[string]PersistedRuntime),
		},
	}
	if err := readSecureJSON(path, &store.state); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("load runtime state: %w", err)
		}
	}
	if store.state.Version != stateVersion {
		return nil, fmt.Errorf("unsupported runtime state version %d", store.state.Version)
	}
	if store.state.Boxes == nil {
		store.state.Boxes = make(map[string]PersistedRuntime)
	}

	changed := false
	now := time.Now().UTC()
	for boxID, record := range store.state.Boxes {
		if record.Status != "exited" && record.Status != "failed" {
			record.Status = "exited"
			record.ProcessID = 0
			record.UpdatedAt = now
			store.state.Boxes[boxID] = record
			changed = true
			continue
		}
		if !record.UpdatedAt.IsZero() && now.Sub(record.UpdatedAt) > retention {
			delete(store.state.Boxes, boxID)
			_ = os.Remove(filepath.Join(filepath.Dir(path), "prompts", boxID+".md"))
			changed = true
		}
	}
	if changed {
		if err := store.saveLocked(); err != nil {
			return nil, err
		}
	}
	return store, nil
}

func (s *StateStore) WritePromptFile(boxID, content string) (string, error) {
	if s == nil || boxID == "" {
		return "", errors.New("box ID is required for prompt materialization")
	}
	path := filepath.Join(filepath.Dir(s.path), "prompts", boxID+".md")
	if err := writeAtomic(path, []byte(content), 0o600); err != nil {
		return "", fmt.Errorf("write system prompt: %w", err)
	}
	return path, nil
}

func (s *StateStore) Get(boxID string) (PersistedRuntime, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.state.Boxes[boxID]
	return record, ok
}

func (s *StateStore) Put(record PersistedRuntime) error {
	if record.BoxID == "" || record.RuntimeInstanceID == "" {
		return errors.New("runtime state requires box and runtime instance IDs")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	previous, existed := s.state.Boxes[record.BoxID]
	record.UpdatedAt = time.Now().UTC()
	s.state.Boxes[record.BoxID] = record
	if err := s.saveLocked(); err != nil {
		if existed {
			s.state.Boxes[record.BoxID] = previous
		} else {
			delete(s.state.Boxes, record.BoxID)
		}
		return err
	}
	return nil
}

func (s *StateStore) Snapshot() []PersistedRuntime {
	s.mu.Lock()
	defer s.mu.Unlock()
	values := make([]PersistedRuntime, 0, len(s.state.Boxes))
	for _, record := range s.state.Boxes {
		values = append(values, record)
	}
	return values
}

func (s *StateStore) saveLocked() error {
	encoded, err := json.Marshal(s.state)
	if err != nil {
		return fmt.Errorf("encode runtime state: %w", err)
	}
	if len(encoded) > maxStateFileBytes {
		return errors.New("runtime state exceeds size limit")
	}
	return writeAtomic(s.path, append(encoded, '\n'), 0o600)
}

type daemonIdentity struct {
	InstanceID string `json:"instanceId"`
}

func LoadOrCreateInstanceID(path string) (string, error) {
	var identity daemonIdentity
	if err := readSecureJSON(path, &identity); err == nil {
		if identity.InstanceID == "" {
			return "", errors.New("daemon identity contains an empty instance ID")
		}
		return identity.InstanceID, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("load daemon identity: %w", err)
	}
	identity.InstanceID = uuid.NewString()
	encoded, err := json.Marshal(identity)
	if err != nil {
		return "", err
	}
	if err := writeAtomic(path, append(encoded, '\n'), 0o600); err != nil {
		return "", fmt.Errorf("persist daemon identity: %w", err)
	}
	return identity.InstanceID, nil
}

type idempotencyState struct {
	NextOrder uint64                              `json:"nextOrder"`
	Frames    map[string]uint64                   `json:"frames"`
	Commands  map[string]hostclient.CommandRecord `json:"commands"`
}

// DurableIdempotencyStore is daemon-owned so interrupted STARTED commands can
// be converted to a terminal result on restart. This closes the otherwise
// permanent STARTED state while still guaranteeing that an uncertain external
// side effect is never repeated.
type DurableIdempotencyStore struct {
	mu     sync.Mutex
	path   string
	limits hostclient.IdempotencyLimits
	state  idempotencyState
}

func OpenDurableIdempotencyStore(path string, limits hostclient.IdempotencyLimits) (*DurableIdempotencyStore, error) {
	if limits.MaxFrameIDs <= 0 || limits.MaxCommands <= 0 {
		return nil, errors.New("idempotency limits must be positive")
	}
	store := &DurableIdempotencyStore{
		path:   path,
		limits: limits,
		state: idempotencyState{
			Frames:   make(map[string]uint64),
			Commands: make(map[string]hostclient.CommandRecord),
		},
	}
	if err := readSecureJSON(path, &store.state); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("load idempotency state: %w", err)
		}
	}
	if store.state.Frames == nil {
		store.state.Frames = make(map[string]uint64)
	}
	if store.state.Commands == nil {
		store.state.Commands = make(map[string]hostclient.CommandRecord)
	}
	if len(store.state.Frames) > limits.MaxFrameIDs || len(store.state.Commands) > limits.MaxCommands {
		return nil, errors.New("idempotency state exceeds configured limits")
	}
	changed := false
	for key, record := range store.state.Commands {
		if record.Stage == hostv1.CommandStage_COMMAND_STAGE_STARTED {
			record.Stage = hostv1.CommandStage_COMMAND_STAGE_CANCELLED
			record.ErrorCode = "daemon_restarted"
			record.ErrorMessage = "daemon restarted after command side effects may have begun; command was not repeated"
			record.ResultJSON = nil
			record.UpdatedAt = time.Now().UTC()
			store.state.Commands[key] = record
			changed = true
		}
	}
	if changed {
		if err := store.saveLocked(); err != nil {
			return nil, err
		}
	}
	return store, nil
}

func (s *DurableIdempotencyStore) FrameSeen(ctx context.Context, frameID string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if frameID == "" {
		return false, errors.New("server frame ID is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, exists := s.state.Frames[frameID]
	return exists, nil
}

func (s *DurableIdempotencyStore) RememberFrame(ctx context.Context, frameID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if frameID == "" {
		return errors.New("server frame ID is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.state.Frames[frameID]; exists {
		return nil
	}
	if len(s.state.Frames) >= s.limits.MaxFrameIDs {
		return errors.New("server frame idempotency store is full")
	}
	s.state.NextOrder++
	s.state.Frames[frameID] = s.state.NextOrder
	if err := s.saveLocked(); err != nil {
		delete(s.state.Frames, frameID)
		s.state.NextOrder--
		return err
	}
	return nil
}

func (s *DurableIdempotencyStore) BeginCommand(ctx context.Context, key, commandID string) (hostclient.CommandRecord, bool, error) {
	if err := ctx.Err(); err != nil {
		return hostclient.CommandRecord{}, false, err
	}
	if key == "" || commandID == "" {
		return hostclient.CommandRecord{}, false, errors.New("command key and ID are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.state.Commands[key]; ok {
		if existing.CommandID != commandID {
			return hostclient.CommandRecord{}, false, fmt.Errorf("idempotency key %q belongs to command %q", key, existing.CommandID)
		}
		return cloneCommandRecord(existing), false, nil
	}
	for _, existing := range s.state.Commands {
		if existing.CommandID == commandID {
			return cloneCommandRecord(existing), false, nil
		}
	}
	if len(s.state.Commands) >= s.limits.MaxCommands {
		return hostclient.CommandRecord{}, false, errors.New("command idempotency store is full")
	}
	record := hostclient.CommandRecord{
		Key:       key,
		CommandID: commandID,
		Stage:     hostv1.CommandStage_COMMAND_STAGE_ACCEPTED,
		UpdatedAt: time.Now().UTC(),
	}
	s.state.Commands[key] = record
	if err := s.saveLocked(); err != nil {
		delete(s.state.Commands, key)
		return hostclient.CommandRecord{}, false, err
	}
	return cloneCommandRecord(record), true, nil
}

func (s *DurableIdempotencyStore) StartCommand(ctx context.Context, key, commandID string) (hostclient.CommandRecord, bool, error) {
	if err := ctx.Err(); err != nil {
		return hostclient.CommandRecord{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.state.Commands[key]
	if !ok || current.CommandID != commandID {
		return hostclient.CommandRecord{}, false, errors.New("unknown or mismatched command")
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
		return hostclient.CommandRecord{}, false, err
	}
	return cloneCommandRecord(started), true, nil
}

func (s *DurableIdempotencyStore) SetCommandResult(ctx context.Context, key string, result hostclient.CommandRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.state.Commands[key]
	if !ok {
		return errors.New("unknown command key")
	}
	if result.CommandID != "" && result.CommandID != current.CommandID {
		return errors.New("command ID changed")
	}
	if !terminalCommandStage(result.Stage) {
		return errors.New("command result is not terminal")
	}
	if terminalCommandStage(current.Stage) && current.Stage != result.Stage {
		return errors.New("terminal command result cannot change")
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

func (s *DurableIdempotencyStore) AbandonCommand(ctx context.Context, key, commandID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.state.Commands[key]
	if !ok {
		return nil
	}
	if current.CommandID != commandID || current.Stage != hostv1.CommandStage_COMMAND_STAGE_ACCEPTED {
		return errors.New("only a matching accepted command can be abandoned")
	}
	delete(s.state.Commands, key)
	if err := s.saveLocked(); err != nil {
		s.state.Commands[key] = current
		return err
	}
	return nil
}

func (s *DurableIdempotencyStore) saveLocked() error {
	encoded, err := json.Marshal(s.state)
	if err != nil {
		return fmt.Errorf("encode idempotency state: %w", err)
	}
	if len(encoded) > maxStateFileBytes {
		return errors.New("idempotency state exceeds size limit")
	}
	return writeAtomic(s.path, append(encoded, '\n'), 0o600)
}

func cloneCommandRecord(record hostclient.CommandRecord) hostclient.CommandRecord {
	record.ResultJSON = append([]byte(nil), record.ResultJSON...)
	return record
}

func terminalCommandStage(stage hostv1.CommandStage) bool {
	return stage == hostv1.CommandStage_COMMAND_STAGE_COMPLETED ||
		stage == hostv1.CommandStage_COMMAND_STAGE_FAILED ||
		stage == hostv1.CommandStage_COMMAND_STAGE_CANCELLED
}

func readSecureJSON(path string, destination any) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return errors.New("state file is accessible by group or other users")
	}
	if info.Size() > maxStateFileBytes {
		return errors.New("state file exceeds size limit")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxStateFileBytes+1))
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("state file contains multiple JSON values")
		}
		return err
	}
	return nil
}

func writeAtomic(path string, data []byte, mode os.FileMode) (returnErr error) {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(directory, "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	temporaryPath := file.Name()
	defer func() {
		if returnErr != nil {
			_ = file.Close()
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := file.Chmod(mode); err != nil {
		return err
	}
	for len(data) > 0 {
		written, err := file.Write(data)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	dir, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return dir.Sync()
}

var _ hostclient.IdempotencyStore = (*DurableIdempotencyStore)(nil)

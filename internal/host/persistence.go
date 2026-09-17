package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
)

const maxStateFileBytes = 4 << 20

type AckStore interface {
	Load(ctx context.Context, daemonInstanceID string) (uint64, error)
	Save(ctx context.Context, daemonInstanceID string, hostSeq uint64) error
}

type Credential struct {
	HostID string `json:"hostId"`
	Secret string `json:"secret"`
}

type CredentialStore interface {
	Load(ctx context.Context) (Credential, error)
	Save(ctx context.Context, credential Credential) error
}

type FileAckStore struct {
	mu     sync.Mutex
	path   string
	loaded bool
	values map[string]uint64
}

func NewFileAckStore(path string) (*FileAckStore, error) {
	if path == "" {
		return nil, protocolError("create ack store", CodeConfiguration, errors.New("ack path is required"))
	}
	return &FileAckStore{path: path, values: make(map[string]uint64)}, nil
}

func (s *FileAckStore) Load(ctx context.Context, daemonInstanceID string) (uint64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if daemonInstanceID == "" {
		return 0, protocolError("load ack", CodeConfiguration, errors.New("daemon instance id is required"))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(); err != nil {
		return 0, err
	}
	return s.values[daemonInstanceID], nil
}

func (s *FileAckStore) Save(ctx context.Context, daemonInstanceID string, hostSeq uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if daemonInstanceID == "" {
		return protocolError("save ack", CodeConfiguration, errors.New("daemon instance id is required"))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(); err != nil {
		return err
	}
	current := s.values[daemonInstanceID]
	if hostSeq <= current {
		return nil
	}
	s.values[daemonInstanceID] = hostSeq
	encoded, err := json.Marshal(struct {
		Acks map[string]uint64 `json:"acks"`
	}{Acks: s.values})
	if err != nil {
		s.values[daemonInstanceID] = current
		return protocolError("encode ack state", CodePersistence, err)
	}
	if err := writeFileAtomic(s.path, append(encoded, '\n'), 0o600); err != nil {
		s.values[daemonInstanceID] = current
		return protocolError("save ack", CodePersistence, err)
	}
	return nil
}

func (s *FileAckStore) loadLocked() error {
	if s.loaded {
		return nil
	}
	var state struct {
		Acks map[string]uint64 `json:"acks"`
	}
	if err := readBoundedJSON(s.path, maxStateFileBytes, &state); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			s.loaded = true
			return nil
		}
		return protocolError("load ack state", CodePersistence, err)
	}
	if state.Acks != nil {
		s.values = state.Acks
	}
	s.loaded = true
	return nil
}

type FileCredentialStore struct {
	mu     sync.Mutex
	path   string
	loaded bool
	value  Credential
}

func NewFileCredentialStore(path string) (*FileCredentialStore, error) {
	if path == "" {
		return nil, protocolError("create credential store", CodeConfiguration, errors.New("credential path is required"))
	}
	return &FileCredentialStore{path: path}, nil
}

func (s *FileCredentialStore) Load(ctx context.Context) (Credential, error) {
	if err := ctx.Err(); err != nil {
		return Credential{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(); err != nil {
		return Credential{}, err
	}
	return s.value, nil
}

func (s *FileCredentialStore) Save(ctx context.Context, credential Credential) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if credential.HostID == "" || credential.Secret == "" {
		return protocolError("save credential", CodeConfiguration, errors.New("host id and credential are required"))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	encoded, err := json.Marshal(credential)
	if err != nil {
		return protocolError("encode credential", CodePersistence, err)
	}
	if err := writeFileAtomic(s.path, append(encoded, '\n'), 0o600); err != nil {
		return protocolError("save credential", CodePersistence, err)
	}
	s.value = credential
	s.loaded = true
	return nil
}

func (s *FileCredentialStore) loadLocked() error {
	if info, err := os.Stat(s.path); err == nil {
		if info.Mode().Perm()&0o077 != 0 {
			return protocolError("load credential", CodeAuthentication, errors.New("credential file must not be accessible by group or other users"))
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return protocolError("load credential", CodePersistence, err)
	}
	if s.loaded {
		return nil
	}
	var credential Credential
	if err := readBoundedJSON(s.path, maxStateFileBytes, &credential); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			s.loaded = true
			return nil
		}
		return protocolError("load credential", CodePersistence, err)
	}
	if (credential.HostID == "") != (credential.Secret == "") {
		return protocolError("load credential", CodePersistence, errors.New("credential file contains an incomplete credential"))
	}
	s.value = credential
	s.loaded = true
	return nil
}

func readBoundedJSON(path string, limit int64, destination any) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if info.Size() > limit {
		return fmt.Errorf("state file is %d bytes; limit is %d", info.Size(), limit)
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
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

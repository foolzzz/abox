package host

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	hostv1 "agentbox/api"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
)

type JournalLimits struct {
	MaxBytes       int64
	MaxRecords     int
	MaxRecordBytes int
}

func DefaultJournalLimits() JournalLimits {
	return JournalLimits{
		MaxBytes:       64 << 20,
		MaxRecords:     16_384,
		MaxRecordBytes: 4 << 20,
	}
}

type FrameIDGenerator func() string

type journalRecord struct {
	HostSeq uint64 `json:"hostSeq"`
	FrameID string `json:"frameId"`
	Frame   []byte `json:"frame"`
}

type Journal struct {
	mu          sync.Mutex
	path        string
	file        *os.File
	limits      JournalLimits
	newFrameID  FrameIDGenerator
	records     []journalRecord
	frameIDs    map[string]struct{}
	bytesOnDisk int64
	nextSeq     uint64
	closed      bool
}

func OpenJournal(path string, limits JournalLimits, newFrameID FrameIDGenerator) (*Journal, error) {
	if path == "" {
		return nil, protocolError("open journal", CodeConfiguration, errors.New("journal path is required"))
	}
	if limits.MaxBytes <= 0 || limits.MaxRecords <= 0 || limits.MaxRecordBytes <= 0 {
		return nil, protocolError("open journal", CodeConfiguration, errors.New("journal limits must be positive"))
	}
	if int64(limits.MaxRecordBytes) > limits.MaxBytes {
		return nil, protocolError("open journal", CodeConfiguration, errors.New("maximum record size exceeds journal size"))
	}
	if newFrameID == nil {
		newFrameID = uuid.NewString
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, protocolError("open journal", CodePersistence, err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		return nil, protocolError("open journal", CodePersistence, err)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, protocolError("open journal", CodePersistence, err)
	}

	journal := &Journal{
		path:       path,
		file:       file,
		limits:     limits,
		newFrameID: newFrameID,
		frameIDs:   make(map[string]struct{}),
		nextSeq:    1,
	}
	if err := journal.load(); err != nil {
		_ = file.Close()
		return nil, err
	}
	return journal, nil
}

func (j *Journal) load() error {
	info, err := j.file.Stat()
	if err != nil {
		return protocolError("read journal metadata", CodePersistence, err)
	}
	if info.Size() > j.limits.MaxBytes {
		return protocolError("load journal", CodeJournalFull, fmt.Errorf("%w: %d bytes exceeds %d", ErrJournalFull, info.Size(), j.limits.MaxBytes))
	}
	j.bytesOnDisk = info.Size()
	if info.Size() == 0 {
		return nil
	}

	lastByte := []byte{0}
	if _, err := j.file.ReadAt(lastByte, info.Size()-1); err != nil {
		return protocolError("inspect journal tail", CodePersistence, err)
	}
	hasCompleteTail := lastByte[0] == '\n'
	if _, err := j.file.Seek(0, io.SeekStart); err != nil {
		return protocolError("seek journal", CodePersistence, err)
	}

	scanner := bufio.NewScanner(j.file)
	scanner.Buffer(make([]byte, 64<<10), j.limits.MaxRecordBytes+1)
	var validBytes int64
	var previousSeq uint64
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := bytes.Clone(scanner.Bytes())
		if len(line) > j.limits.MaxRecordBytes {
			return protocolError("load journal", CodeJournalCorrupt, fmt.Errorf("%w: line %d exceeds record limit", ErrJournalCorrupt, lineNumber))
		}
		var record journalRecord
		if err := json.Unmarshal(line, &record); err != nil {
			if !hasCompleteTail && validBytes+int64(len(line)) == info.Size() {
				if err := j.file.Truncate(validBytes); err != nil {
					return protocolError("recover journal tail", CodePersistence, err)
				}
				if err := j.file.Sync(); err != nil {
					return protocolError("sync recovered journal", CodePersistence, err)
				}
				j.bytesOnDisk = validBytes
				break
			}
			return protocolError("load journal", CodeJournalCorrupt, fmt.Errorf("%w: decode line %d: %v", ErrJournalCorrupt, lineNumber, err))
		}
		if record.HostSeq == 0 || record.FrameID == "" || (previousSeq > 0 && record.HostSeq != previousSeq+1) {
			return protocolError("load journal", CodeJournalCorrupt, fmt.Errorf("%w: invalid sequence or frame id on line %d", ErrJournalCorrupt, lineNumber))
		}
		if _, duplicate := j.frameIDs[record.FrameID]; duplicate {
			return protocolError("load journal", CodeJournalCorrupt, fmt.Errorf("%w: duplicate frame id on line %d", ErrJournalCorrupt, lineNumber))
		}
		var frame hostv1.HostFrame
		if err := proto.Unmarshal(record.Frame, &frame); err != nil {
			return protocolError("load journal", CodeJournalCorrupt, fmt.Errorf("%w: decode frame on line %d: %v", ErrJournalCorrupt, lineNumber, err))
		}
		if frame.HostSeq != record.HostSeq || frame.FrameId != record.FrameID {
			return protocolError("load journal", CodeJournalCorrupt, fmt.Errorf("%w: envelope mismatch on line %d", ErrJournalCorrupt, lineNumber))
		}
		j.frameIDs[record.FrameID] = struct{}{}
		j.records = append(j.records, record)
		if len(j.records) > j.limits.MaxRecords {
			return protocolError("load journal", CodeJournalFull, fmt.Errorf("%w: record count exceeds %d", ErrJournalFull, j.limits.MaxRecords))
		}
		previousSeq = record.HostSeq
		validBytes += int64(len(line) + 1)
	}
	if err := scanner.Err(); err != nil {
		return protocolError("scan journal", CodeJournalCorrupt, fmt.Errorf("%w: %v", ErrJournalCorrupt, err))
	}
	if !hasCompleteTail && validBytes == info.Size()+1 {
		if info.Size()+1 > j.limits.MaxBytes {
			return protocolError("recover journal tail", CodeJournalFull, ErrJournalFull)
		}
		if err := writeAll(j.file, []byte{'\n'}); err != nil {
			return protocolError("recover journal tail", CodePersistence, err)
		}
		if err := j.file.Sync(); err != nil {
			return protocolError("sync recovered journal", CodePersistence, err)
		}
		j.bytesOnDisk++
	}
	if previousSeq > 0 {
		j.nextSeq = previousSeq + 1
	}
	if _, err := j.file.Seek(0, io.SeekEnd); err != nil {
		return protocolError("seek journal end", CodePersistence, err)
	}
	return nil
}

func (j *Journal) Prepare(lastAcked uint64) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return protocolError("prepare journal", CodePersistence, os.ErrClosed)
	}
	if j.nextSeq <= lastAcked {
		j.nextSeq = lastAcked + 1
	}
	if err := j.compactLocked(lastAcked); err != nil {
		return err
	}
	if len(j.records) > 0 && j.records[0].HostSeq != lastAcked+1 {
		return protocolError("prepare journal", CodeJournalCorrupt, fmt.Errorf("%w: first unacked sequence is %d after ack %d", ErrJournalCorrupt, j.records[0].HostSeq, lastAcked))
	}
	return nil
}

func (j *Journal) Append(frame *hostv1.HostFrame) (*hostv1.HostFrame, error) {
	if frame == nil || frame.Payload == nil {
		return nil, protocolError("append frame", CodeProtocol, errors.New("frame payload is required"))
	}

	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return nil, protocolError("append frame", CodePersistence, os.ErrClosed)
	}
	if len(j.records) >= j.limits.MaxRecords {
		return nil, protocolError("append frame", CodeJournalFull, ErrJournalFull)
	}

	stored := proto.Clone(frame).(*hostv1.HostFrame)
	stored.HostSeq = j.nextSeq
	if stored.FrameId == "" {
		stored.FrameId = j.newFrameID()
	}
	if stored.FrameId == "" {
		return nil, protocolError("append frame", CodeConfiguration, errors.New("frame id generator returned an empty id"))
	}
	if _, duplicate := j.frameIDs[stored.FrameId]; duplicate {
		return nil, protocolError("append frame", CodeProtocol, fmt.Errorf("duplicate frame id %q", stored.FrameId))
	}
	encodedFrame, err := proto.Marshal(stored)
	if err != nil {
		return nil, protocolError("encode frame", CodeProtocol, err)
	}
	record := journalRecord{HostSeq: stored.HostSeq, FrameID: stored.FrameId, Frame: encodedFrame}
	line, err := json.Marshal(record)
	if err != nil {
		return nil, protocolError("encode journal record", CodePersistence, err)
	}
	if len(line) > j.limits.MaxRecordBytes {
		return nil, protocolError("append frame", CodeJournalFull, fmt.Errorf("%w: record is %d bytes", ErrJournalFull, len(line)))
	}
	line = append(line, '\n')
	if j.bytesOnDisk+int64(len(line)) > j.limits.MaxBytes {
		return nil, protocolError("append frame", CodeJournalFull, ErrJournalFull)
	}
	if err := writeAll(j.file, line); err != nil {
		j.closed = true
		_ = j.file.Close()
		return nil, protocolError("append frame", CodePersistence, err)
	}
	if err := j.file.Sync(); err != nil {
		j.closed = true
		_ = j.file.Close()
		return nil, protocolError("sync frame", CodePersistence, err)
	}
	j.records = append(j.records, record)
	j.frameIDs[record.FrameID] = struct{}{}
	j.bytesOnDisk += int64(len(line))
	j.nextSeq++
	return proto.Clone(stored).(*hostv1.HostFrame), nil
}

func (j *Journal) EntriesAfter(hostSeq uint64) ([]*hostv1.HostFrame, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return nil, protocolError("read journal", CodePersistence, os.ErrClosed)
	}
	frames := make([]*hostv1.HostFrame, 0, len(j.records))
	for _, record := range j.records {
		if record.HostSeq <= hostSeq {
			continue
		}
		var frame hostv1.HostFrame
		if err := proto.Unmarshal(record.Frame, &frame); err != nil {
			return nil, protocolError("decode journal frame", CodeJournalCorrupt, fmt.Errorf("%w: %v", ErrJournalCorrupt, err))
		}
		frames = append(frames, &frame)
	}
	return frames, nil
}

func (j *Journal) Acknowledge(hostSeq uint64) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return protocolError("acknowledge journal", CodePersistence, os.ErrClosed)
	}
	return j.compactLocked(hostSeq)
}

func (j *Journal) compactLocked(hostSeq uint64) error {
	firstRemaining := 0
	for firstRemaining < len(j.records) && j.records[firstRemaining].HostSeq <= hostSeq {
		firstRemaining++
	}
	if firstRemaining == 0 {
		return nil
	}
	remaining := append([]journalRecord(nil), j.records[firstRemaining:]...)
	compactedBytes, committed, compactionErr := rewriteJournal(j.path, remaining)
	if compactionErr != nil && !committed {
		return protocolError("compact journal", CodePersistence, compactionErr)
	}
	closeErr := j.file.Close()
	file, err := os.OpenFile(j.path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		j.closed = true
		return protocolError("reopen compacted journal", CodePersistence, errors.Join(compactionErr, err))
	}
	j.file = file
	j.records = remaining
	j.frameIDs = make(map[string]struct{}, len(remaining))
	for _, record := range remaining {
		j.frameIDs[record.FrameID] = struct{}{}
	}
	j.bytesOnDisk = compactedBytes
	if compactionErr != nil || closeErr != nil {
		return protocolError("finish journal compaction", CodePersistence, errors.Join(compactionErr, closeErr))
	}
	return nil
}

func rewriteJournal(path string, records []journalRecord) (size int64, committed bool, retErr error) {
	directory := filepath.Dir(path)
	file, err := os.CreateTemp(directory, "."+filepath.Base(path)+"-compact-*")
	if err != nil {
		return 0, false, err
	}
	temporaryPath := file.Name()
	defer func() {
		if retErr != nil {
			_ = file.Close()
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		return 0, false, err
	}
	for _, record := range records {
		line, err := json.Marshal(record)
		if err != nil {
			return 0, false, err
		}
		line = append(line, '\n')
		if err := writeAll(file, line); err != nil {
			return 0, false, err
		}
		size += int64(len(line))
	}
	if err := file.Sync(); err != nil {
		return 0, false, err
	}
	if err := file.Close(); err != nil {
		return 0, false, err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return 0, false, err
	}
	committed = true
	if err := syncDirectory(directory); err != nil {
		return size, true, err
	}
	return size, true, nil
}

func (j *Journal) LastSequence() uint64 {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.nextSeq == 0 {
		return 0
	}
	return j.nextSeq - 1
}

type JournalStats struct {
	Records int
	Bytes   int64
	Closed  bool
}

func (j *Journal) Stats() JournalStats {
	j.mu.Lock()
	defer j.mu.Unlock()
	return JournalStats{
		Records: len(j.records),
		Bytes:   j.bytesOnDisk,
		Closed:  j.closed,
	}
}

func (j *Journal) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return nil
	}
	j.closed = true
	if err := j.file.Sync(); err != nil {
		_ = j.file.Close()
		return protocolError("close journal", CodePersistence, err)
	}
	if err := j.file.Close(); err != nil {
		return protocolError("close journal", CodePersistence, err)
	}
	return nil
}

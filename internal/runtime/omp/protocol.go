package omp

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"
)

type frameHeader struct {
	Type string `json:"type"`
}

type readyFrame struct {
	Type                      string `json:"type"`
	ProtocolVersion           int    `json:"protocolVersion"`
	SupportedProtocolVersions []int  `json:"supportedProtocolVersions"`
	MaxFrameBytes             int    `json:"maxFrameBytes"`
	MaxReassembledFrameBytes  int    `json:"maxReassembledFrameBytes"`
}

func (f readyFrame) supports(version int) bool {
	for _, supported := range f.SupportedProtocolVersions {
		if supported == version {
			return true
		}
	}
	return false
}

type rpcChunk struct {
	Type       string `json:"type"`
	ChunkID    string `json:"chunkId"`
	Index      int    `json:"index"`
	Count      int    `json:"count"`
	ByteLength int    `json:"byteLength"`
	Data       string `json:"data"`
}

type chunkSequence struct {
	id         string
	nextIndex  int
	count      int
	byteLength int
	data       []byte
}

type frameDecoder struct {
	maxFrameBytes            int
	maxReassembledFrameBytes int
	allowChunks              bool
	chunks                   *chunkSequence
}

func newFrameDecoder(maxFrameBytes, maxReassembledFrameBytes int) *frameDecoder {
	return &frameDecoder{
		maxFrameBytes:            maxFrameBytes,
		maxReassembledFrameBytes: maxReassembledFrameBytes,
	}
}

func (d *frameDecoder) setLimits(maxFrameBytes, maxReassembledFrameBytes int) {
	if maxFrameBytes > 0 && maxFrameBytes < d.maxFrameBytes {
		d.maxFrameBytes = maxFrameBytes
	}
	if maxReassembledFrameBytes > 0 && maxReassembledFrameBytes < d.maxReassembledFrameBytes {
		d.maxReassembledFrameBytes = maxReassembledFrameBytes
	}
}

func (d *frameDecoder) enableChunks() {
	d.allowChunks = true
}

// consume returns a complete logical JSON frame. A nil frame means that a
// validated chunk sequence is still in progress.
func (d *frameDecoder) consume(line []byte) (json.RawMessage, error) {
	if len(line) > d.maxFrameBytes {
		return nil, &Error{Op: "decode", Code: codeFrameTooLarge, Err: fmt.Errorf("physical frame is %d bytes; limit is %d", len(line), d.maxFrameBytes)}
	}
	if len(bytes.TrimSpace(line)) == 0 {
		return nil, &Error{Op: "decode", Code: codeProtocol, Err: errors.New("empty RPC frame")}
	}
	if !utf8.Valid(line) {
		return nil, &Error{Op: "decode", Code: codeProtocol, Err: errors.New("RPC frame is not valid UTF-8")}
	}

	var header frameHeader
	if err := json.Unmarshal(line, &header); err != nil {
		return nil, &Error{Op: "decode", Code: codeProtocol, Err: fmt.Errorf("malformed JSON frame: %w", err)}
	}
	if header.Type == "" {
		return nil, &Error{Op: "decode", Code: codeProtocol, Err: errors.New("RPC frame has no string type")}
	}

	if header.Type != "rpc_chunk" {
		if d.chunks != nil {
			return nil, &Error{Op: "decode", Code: codeProtocol, Err: fmt.Errorf("chunk sequence %q was interrupted at index %d", d.chunks.id, d.chunks.nextIndex)}
		}
		return append(json.RawMessage(nil), line...), nil
	}
	if !d.allowChunks {
		return nil, &Error{Op: "decode", Code: codeProtocol, Err: errors.New("received rpc_chunk before protocol v2 negotiation")}
	}

	var chunk rpcChunk
	if err := json.Unmarshal(line, &chunk); err != nil {
		return nil, &Error{Op: "decode", Code: codeProtocol, Err: fmt.Errorf("malformed rpc_chunk: %w", err)}
	}
	var chunkFields map[string]json.RawMessage
	if err := json.Unmarshal(line, &chunkFields); err != nil {
		return nil, &Error{Op: "decode", Code: codeProtocol, Err: fmt.Errorf("malformed rpc_chunk object: %w", err)}
	}
	for _, field := range []string{"chunkId", "index", "count", "byteLength", "data"} {
		if _, ok := chunkFields[field]; !ok {
			return nil, &Error{Op: "decode", Code: codeProtocol, Err: fmt.Errorf("rpc_chunk is missing %s", field)}
		}
	}
	if err := d.validateChunk(chunk); err != nil {
		return nil, err
	}

	decoded, err := base64.StdEncoding.DecodeString(chunk.Data)
	if err != nil {
		d.chunks = nil
		return nil, &Error{Op: "decode", Code: codeProtocol, Err: fmt.Errorf("chunk %q index %d has invalid base64: %w", chunk.ChunkID, chunk.Index, err)}
	}
	if len(decoded) > chunk.ByteLength-len(d.chunks.data) {
		d.chunks = nil
		return nil, &Error{Op: "decode", Code: codeProtocol, Err: fmt.Errorf("chunk %q index %d exceeds declared byte length", chunk.ChunkID, chunk.Index)}
	}
	d.chunks.data = append(d.chunks.data, decoded...)
	d.chunks.nextIndex++

	if d.chunks.nextIndex < d.chunks.count {
		return nil, nil
	}

	sequence := d.chunks
	d.chunks = nil
	if len(sequence.data) != sequence.byteLength {
		return nil, &Error{Op: "decode", Code: codeProtocol, Err: fmt.Errorf("chunk sequence %q decoded to %d bytes; expected %d", sequence.id, len(sequence.data), sequence.byteLength)}
	}
	if !utf8.Valid(sequence.data) {
		return nil, &Error{Op: "decode", Code: codeProtocol, Err: fmt.Errorf("chunk sequence %q is not valid UTF-8", sequence.id)}
	}

	var reassembledHeader frameHeader
	if err := json.Unmarshal(sequence.data, &reassembledHeader); err != nil {
		return nil, &Error{Op: "decode", Code: codeProtocol, Err: fmt.Errorf("chunk sequence %q is not a JSON frame: %w", sequence.id, err)}
	}
	if reassembledHeader.Type == "" || reassembledHeader.Type == "rpc_chunk" {
		return nil, &Error{Op: "decode", Code: codeProtocol, Err: fmt.Errorf("chunk sequence %q has invalid frame type %q", sequence.id, reassembledHeader.Type)}
	}
	return append(json.RawMessage(nil), sequence.data...), nil
}

func (d *frameDecoder) validateChunk(chunk rpcChunk) error {
	if chunk.ChunkID == "" {
		return &Error{Op: "decode", Code: codeProtocol, Err: errors.New("rpc_chunk has an empty chunkId")}
	}
	if chunk.Data == "" {
		return &Error{Op: "decode", Code: codeProtocol, Err: fmt.Errorf("chunk %q index %d has empty data", chunk.ChunkID, chunk.Index)}
	}
	if chunk.Count <= 0 {
		return &Error{Op: "decode", Code: codeProtocol, Err: fmt.Errorf("chunk %q has invalid count %d", chunk.ChunkID, chunk.Count)}
	}
	if chunk.ByteLength <= 0 {
		return &Error{Op: "decode", Code: codeProtocol, Err: fmt.Errorf("chunk %q has invalid byteLength %d", chunk.ChunkID, chunk.ByteLength)}
	}
	if chunk.ByteLength > d.maxReassembledFrameBytes {
		return &Error{Op: "decode", Code: codeFrameTooLarge, Err: fmt.Errorf("chunk %q declares %d bytes; limit is %d", chunk.ChunkID, chunk.ByteLength, d.maxReassembledFrameBytes)}
	}
	if chunk.Count > chunk.ByteLength {
		return &Error{Op: "decode", Code: codeProtocol, Err: fmt.Errorf("chunk %q count %d exceeds byte length %d", chunk.ChunkID, chunk.Count, chunk.ByteLength)}
	}

	if d.chunks == nil {
		if chunk.Index != 0 {
			return &Error{Op: "decode", Code: codeProtocol, Err: fmt.Errorf("chunk sequence %q starts at index %d", chunk.ChunkID, chunk.Index)}
		}
		d.chunks = &chunkSequence{
			id:         chunk.ChunkID,
			count:      chunk.Count,
			byteLength: chunk.ByteLength,
			data:       make([]byte, 0, chunk.ByteLength),
		}
		return nil
	}

	sequence := d.chunks
	if chunk.ChunkID != sequence.id {
		return &Error{Op: "decode", Code: codeProtocol, Err: fmt.Errorf("chunk sequence %q was interleaved with %q", sequence.id, chunk.ChunkID)}
	}
	if chunk.Count != sequence.count || chunk.ByteLength != sequence.byteLength {
		return &Error{Op: "decode", Code: codeProtocol, Err: fmt.Errorf("chunk sequence %q metadata changed", sequence.id)}
	}
	if chunk.Index != sequence.nextIndex {
		return &Error{Op: "decode", Code: codeProtocol, Err: fmt.Errorf("chunk sequence %q expected index %d; got %d", sequence.id, sequence.nextIndex, chunk.Index)}
	}
	return nil
}

func (d *frameDecoder) finish() error {
	if d.chunks == nil {
		return nil
	}
	return &Error{Op: "decode", Code: codeProtocol, Err: fmt.Errorf("chunk sequence %q ended at index %d of %d", d.chunks.id, d.chunks.nextIndex, d.chunks.count)}
}

func parseReadyFrame(raw json.RawMessage, configuredMaxFrameBytes, configuredMaxReassembledFrameBytes int) (readyFrame, error) {
	var ready readyFrame
	if err := json.Unmarshal(raw, &ready); err != nil {
		return readyFrame{}, &Error{Op: "ready", Code: codeProtocol, Err: fmt.Errorf("decode ready frame: %w", err)}
	}
	if ready.Type != "ready" || ready.ProtocolVersion != 1 {
		return readyFrame{}, &Error{Op: "ready", Code: codeProtocol, Err: fmt.Errorf("unsupported ready frame version %d", ready.ProtocolVersion)}
	}
	if ready.MaxFrameBytes == 0 {
		ready.MaxFrameBytes = configuredMaxFrameBytes
	}
	if ready.MaxReassembledFrameBytes == 0 {
		ready.MaxReassembledFrameBytes = configuredMaxReassembledFrameBytes
	}
	if ready.MaxFrameBytes <= 0 || ready.MaxReassembledFrameBytes <= 0 {
		return readyFrame{}, &Error{Op: "ready", Code: codeProtocol, Err: errors.New("ready frame advertises invalid transport limits")}
	}
	return ready, nil
}

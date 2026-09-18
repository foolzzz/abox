package codex

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
)

func TestWriteLineHandlesPartialWrites(t *testing.T) {
	writer := &chunkWriteCloser{limit: 2}
	handle := &processHandle{stdin: writer, done: make(chan struct{})}

	if err := handle.writeLine(context.Background(), []byte("request")); err != nil {
		t.Fatalf("write line: %v", err)
	}
	if got, want := writer.String(), "request\n"; got != want {
		t.Fatalf("written data = %q, want %q", got, want)
	}
}

func TestWriteLineRejectsWriterWithoutProgress(t *testing.T) {
	handle := &processHandle{stdin: zeroWriteCloser{}, done: make(chan struct{})}

	err := handle.writeLine(context.Background(), []byte("request"))
	if !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("error = %v, want %v", err, io.ErrShortWrite)
	}
}

type chunkWriteCloser struct {
	bytes.Buffer
	limit int
}

func (w *chunkWriteCloser) Write(payload []byte) (int, error) {
	if len(payload) > w.limit {
		payload = payload[:w.limit]
	}
	return w.Buffer.Write(payload)
}

func (w *chunkWriteCloser) Close() error { return nil }

type zeroWriteCloser struct{}

func (zeroWriteCloser) Write([]byte) (int, error) { return 0, nil }
func (zeroWriteCloser) Close() error              { return nil }

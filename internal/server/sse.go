package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"agentbox/internal/domain"
	"github.com/go-chi/chi/v5"
)

type eventSubscriber struct {
	events chan domain.BoxEvent
	done   chan struct{}
}

type eventHub struct {
	mu         sync.Mutex
	byBox      map[string]map[*eventSubscriber]struct{}
	bufferSize int
	metrics    *metrics
}

func newEventHub(bufferSize int, metricSet *metrics) *eventHub {
	return &eventHub{
		byBox:      make(map[string]map[*eventSubscriber]struct{}),
		bufferSize: bufferSize,
		metrics:    metricSet,
	}
}

func (h *eventHub) subscribe(boxID string) *eventSubscriber {
	subscriber := &eventSubscriber{
		events: make(chan domain.BoxEvent, h.bufferSize),
		done:   make(chan struct{}),
	}
	h.mu.Lock()
	if h.byBox[boxID] == nil {
		h.byBox[boxID] = make(map[*eventSubscriber]struct{})
	}
	h.byBox[boxID][subscriber] = struct{}{}
	h.metrics.sseConnections.Add(1)
	h.mu.Unlock()
	return subscriber
}

func (h *eventHub) unsubscribe(boxID string, subscriber *eventSubscriber) {
	h.mu.Lock()
	h.removeLocked(boxID, subscriber, false)
	h.mu.Unlock()
}

func (h *eventHub) publish(event domain.BoxEvent) {
	h.mu.Lock()
	for subscriber := range h.byBox[event.BoxID] {
		select {
		case subscriber.events <- event:
		default:
			h.removeLocked(event.BoxID, subscriber, true)
			h.metrics.eventBroadcastDropped.Add(1)
		}
	}
	h.mu.Unlock()
}

func (h *eventHub) removeLocked(boxID string, subscriber *eventSubscriber, signal bool) {
	subscribers := h.byBox[boxID]
	if _, exists := subscribers[subscriber]; !exists {
		return
	}
	delete(subscribers, subscriber)
	if len(subscribers) == 0 {
		delete(h.byBox, boxID)
	}
	if signal {
		close(subscriber.done)
	}
	h.metrics.sseConnections.Add(-1)
}

func (s *Server) handleEvents(writer http.ResponseWriter, request *http.Request) {
	flusher, ok := writer.(http.Flusher)
	if !ok {
		writeProblem(writer, http.StatusInternalServerError, "streaming_unsupported", "response streaming is unavailable")
		return
	}
	afterSeq, err := eventCursor(request)
	if err != nil {
		writeProblem(writer, http.StatusBadRequest, "invalid_event_cursor", err.Error())
		return
	}
	user, _ := requestUser(request)
	boxID := chi.URLParam(request, "boxID")
	box, err := s.store.GetBox(request.Context(), user, boxID)
	if err != nil {
		s.writeStoreError(writer, "get box event stream", err)
		return
	}
	if afterSeq > box.LastEventSeq {
		writeProblem(writer, http.StatusBadRequest, "invalid_event_cursor", "Last-Event-ID is ahead of the box event stream")
		return
	}
	if box.LastEventSeq-afterSeq > int64(s.replayLimit) {
		writer.Header().Set("X-Agentbox-Snapshot-Required", "true")
		writeProblem(writer, http.StatusConflict, "snapshot_required", "event replay exceeds the server limit; fetch a fresh box snapshot")
		return
	}

	subscriber := s.events.subscribe(boxID)
	defer s.events.unsubscribe(boxID, subscriber)

	replay, err := s.store.ListEvents(request.Context(), user, boxID, afterSeq, s.replayLimit+1)
	if err != nil {
		s.writeStoreError(writer, "replay box events", err)
		return
	}
	if len(replay) > s.replayLimit {
		writer.Header().Set("X-Agentbox-Snapshot-Required", "true")
		writeProblem(writer, http.StatusConflict, "snapshot_required", "event replay exceeds the server limit; fetch a fresh box snapshot")
		return
	}

	writer.Header().Set("Content-Type", "text/event-stream")
	writer.Header().Set("Cache-Control", "no-cache, no-transform")
	writer.Header().Set("Connection", "keep-alive")
	writer.Header().Set("X-Accel-Buffering", "no")
	writer.WriteHeader(http.StatusOK)

	lastSent := afterSeq
	for index := range replay {
		if replay[index].Seq <= lastSent {
			continue
		}
		if err := writeSSE(writer, replay[index]); err != nil {
			return
		}
		lastSent = replay[index].Seq
	}
	flusher.Flush()

	heartbeat := time.NewTicker(20 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-request.Context().Done():
			return
		case <-subscriber.done:
			return
		case event := <-subscriber.events:
			if event.Seq <= lastSent {
				continue
			}
			if event.Seq != lastSent+1 {
				return
			}
			if err := writeSSE(writer, event); err != nil {
				return
			}
			lastSent = event.Seq
			flusher.Flush()
		case <-heartbeat.C:
			if _, err := fmt.Fprintf(writer, ": heartbeat %d\n\n", time.Now().UnixMilli()); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func eventCursor(request *http.Request) (int64, error) {
	value := strings.TrimSpace(request.Header.Get("Last-Event-ID"))
	if value == "" {
		value = strings.TrimSpace(request.URL.Query().Get("after"))
	}
	if value == "" {
		return 0, nil
	}
	sequence, err := strconv.ParseInt(value, 10, 64)
	if err != nil || sequence < 0 {
		return 0, fmt.Errorf("event cursor must be a non-negative integer")
	}
	return sequence, nil
}

func writeSSE(writer http.ResponseWriter, event domain.BoxEvent) error {
	payload := event.Payload
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}
	if _, err := fmt.Fprintf(writer, "id: %d\n", event.Seq); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(writer, "event: %s\n", sanitizeSSEField(event.EventType)); err != nil {
		return err
	}
	for _, line := range strings.Split(string(payload), "\n") {
		if _, err := fmt.Fprintf(writer, "data: %s\n", line); err != nil {
			return err
		}
	}
	_, err := fmt.Fprint(writer, "\n")
	return err
}

func sanitizeSSEField(value string) string {
	value = strings.Map(func(character rune) rune {
		if character == '\r' || character == '\n' || character == 0 {
			return -1
		}
		return character
	}, value)
	if value == "" {
		return "message"
	}
	return value
}

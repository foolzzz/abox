package server

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"
)

type metrics struct {
	httpRequests          atomic.Uint64
	httpDurationNanos     atomic.Uint64
	sseConnections        atomic.Int64
	hostConnections       atomic.Int64
	eventBroadcastDropped atomic.Uint64
	eventsIngested        atomic.Uint64
	commandsDispatched    atomic.Uint64
	reaperErrors          atomic.Uint64
}

func newMetrics() *metrics {
	return &metrics{}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (writer *statusRecorder) WriteHeader(statusCode int) {
	if writer.status != 0 {
		return
	}
	writer.status = statusCode
	writer.ResponseWriter.WriteHeader(statusCode)
}

func (writer *statusRecorder) Flush() {
	if writer.status == 0 {
		writer.WriteHeader(http.StatusOK)
	}
	if flusher, ok := writer.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (writer *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := writer.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("response writer does not support hijacking")
	}
	return hijacker.Hijack()
}

func (writer *statusRecorder) Unwrap() http.ResponseWriter {
	return writer.ResponseWriter
}

func (writer *statusRecorder) Write(body []byte) (int, error) {
	if writer.status == 0 {
		writer.status = http.StatusOK
	}
	return writer.ResponseWriter.Write(body)
}

func (s *Server) observeHTTP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		started := time.Now()
		recorder := &statusRecorder{ResponseWriter: writer}
		next.ServeHTTP(recorder, request)
		s.metrics.httpRequests.Add(1)
		s.metrics.httpDurationNanos.Add(uint64(time.Since(started)))
	})
}

func (m *metrics) handle(writer http.ResponseWriter, _ *http.Request) {
	writer.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	writer.WriteHeader(http.StatusOK)
	seconds := float64(m.httpDurationNanos.Load()) / float64(time.Second)
	_, _ = fmt.Fprintf(writer, `# HELP agentbox_http_requests_total Total HTTP requests served.
# TYPE agentbox_http_requests_total counter
agentbox_http_requests_total %d
# HELP agentbox_http_request_duration_seconds_sum Cumulative HTTP request duration.
# TYPE agentbox_http_request_duration_seconds_sum counter
agentbox_http_request_duration_seconds_sum %s
# HELP agentbox_sse_connections Current SSE connections.
# TYPE agentbox_sse_connections gauge
agentbox_sse_connections %d
# HELP agentbox_host_connections Current daemon connections.
# TYPE agentbox_host_connections gauge
agentbox_host_connections %d
# HELP agentbox_event_broadcast_dropped_total SSE subscribers disconnected due to backpressure.
# TYPE agentbox_event_broadcast_dropped_total counter
agentbox_event_broadcast_dropped_total %d
# HELP agentbox_events_ingested_total Persisted runtime events ingested.
# TYPE agentbox_events_ingested_total counter
agentbox_events_ingested_total %d
# HELP agentbox_commands_dispatched_total Persisted commands offered to connected hosts.
# TYPE agentbox_commands_dispatched_total counter
agentbox_commands_dispatched_total %d
# HELP agentbox_reaper_errors_total Reconciliation worker database errors.
# TYPE agentbox_reaper_errors_total counter
agentbox_reaper_errors_total %d
`,
		m.httpRequests.Load(),
		strconv.FormatFloat(seconds, 'g', -1, 64),
		m.sseConnections.Load(),
		m.hostConnections.Load(),
		m.eventBroadcastDropped.Load(),
		m.eventsIngested.Load(),
		m.commandsDispatched.Load(),
		m.reaperErrors.Load(),
	)
}

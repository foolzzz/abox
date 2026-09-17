package server

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"agentbox/internal/domain"
)

type metrics struct {
	httpRequests          atomic.Uint64
	httpDurationNanos     atomic.Uint64
	httpStatusClasses     [6]atomic.Uint64
	sseConnections        atomic.Int64
	hostConnections       atomic.Int64
	eventBroadcastDropped atomic.Uint64
	eventsIngested        atomic.Uint64
	commandsDispatched    atomic.Uint64
	commandOutcomes       [3]atomic.Uint64
	approvalTimeouts      atomic.Uint64
	scheduleExecutions    atomic.Uint64
	reaperErrors          atomic.Uint64
	activeBoxes           atomic.Int64
	activeRuns            atomic.Int64
	hostInventoryReady    atomic.Int64
	boxInventoryReady     atomic.Int64
	ompAvailable          atomic.Int64
	codexAvailable        atomic.Int64
	claudeAvailable       atomic.Int64
	codexEnabled          bool
	claudeEnabled         bool
	failuresMu            sync.Mutex
	failures              map[string]diagnosticFailure
}

type diagnosticFailure struct {
	Component string    `json:"component"`
	Summary   string    `json:"summary"`
	Observed  time.Time `json:"observedAt"`
}

func newMetrics() *metrics {
	return &metrics{failures: make(map[string]diagnosticFailure)}
}

func (m *metrics) recordFailure(component, summary string) {
	m.failuresMu.Lock()
	m.failures[component] = diagnosticFailure{
		Component: component,
		Summary:   summary,
		Observed:  time.Now().UTC(),
	}
	m.failuresMu.Unlock()
}

func (m *metrics) failureSnapshot() []diagnosticFailure {
	m.failuresMu.Lock()
	result := make([]diagnosticFailure, 0, len(m.failures))
	for _, failure := range m.failures {
		result = append(result, failure)
	}
	m.failuresMu.Unlock()
	sort.Slice(result, func(left, right int) bool {
		return result[left].Component < result[right].Component
	})
	return result
}

func (m *metrics) observeCommandOutcome(outcome string) {
	index := -1
	switch outcome {
	case "completed":
		index = 0
	case "failed":
		index = 1
	case "cancelled":
		index = 2
	}
	if index >= 0 {
		m.commandOutcomes[index].Add(1)
		if outcome == "failed" {
			m.recordFailure("host_command", "host command execution failed")
		}
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (writer *statusRecorder) WriteHeader(statusCode int) {
	if statusCode >= 100 && statusCode < 200 && statusCode != http.StatusSwitchingProtocols {
		writer.ResponseWriter.WriteHeader(statusCode)
		return
	}
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
		statusCode := recorder.status
		if statusCode == 0 {
			statusCode = http.StatusOK
		}
		statusClass := statusCode / 100
		if statusClass >= 1 && statusClass <= 5 {
			s.metrics.httpStatusClasses[statusClass].Add(1)
		}
		s.metrics.httpRequests.Add(1)
		s.metrics.httpDurationNanos.Add(uint64(time.Since(started)))
	})
}

func (s *Server) handleMetrics(writer http.ResponseWriter, request *http.Request) {
	ctx, cancel := context.WithTimeout(request.Context(), 2*time.Second)
	defer cancel()
	s.refreshOperationalGauges(ctx)
	s.metrics.handle(writer, request)
}

func (m *metrics) handle(writer http.ResponseWriter, _ *http.Request) {
	writer.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	writer.WriteHeader(http.StatusOK)
	seconds := float64(m.httpDurationNanos.Load()) / float64(time.Second)
	activeBoxes := metricGaugeValue(m.boxInventoryReady.Load() == 1, m.activeBoxes.Load())
	activeRuns := metricGaugeValue(m.boxInventoryReady.Load() == 1, m.activeRuns.Load())
	ompAvailable := metricGaugeValue(m.hostInventoryReady.Load() == 1, m.ompAvailable.Load())
	optionalRuntimeMetrics := ""
	if m.codexEnabled {
		optionalRuntimeMetrics += fmt.Sprintf("agentbox_runtime_available{runtime=\"codex\"} %s\n",
			metricGaugeValue(m.hostInventoryReady.Load() == 1, m.codexAvailable.Load()))
	}
	if m.claudeEnabled {
		optionalRuntimeMetrics += fmt.Sprintf("agentbox_runtime_available{runtime=\"claude\"} %s\n",
			metricGaugeValue(m.hostInventoryReady.Load() == 1, m.claudeAvailable.Load()))
	}
	_, _ = fmt.Fprintf(writer, `# HELP agentbox_http_requests_total Total HTTP requests served.
# TYPE agentbox_http_requests_total counter
agentbox_http_requests_total %d
# HELP agentbox_http_responses_total Total HTTP responses by status class.
# TYPE agentbox_http_responses_total counter
agentbox_http_responses_total{status_class="1xx"} %d
agentbox_http_responses_total{status_class="2xx"} %d
agentbox_http_responses_total{status_class="3xx"} %d
agentbox_http_responses_total{status_class="4xx"} %d
agentbox_http_responses_total{status_class="5xx"} %d
# HELP agentbox_http_request_duration_seconds_sum Cumulative HTTP request duration.
# TYPE agentbox_http_request_duration_seconds_sum counter
agentbox_http_request_duration_seconds_sum %s
# HELP agentbox_sse_connections Current SSE connections.
# TYPE agentbox_sse_connections gauge
agentbox_sse_connections %d
# HELP agentbox_host_connections Current daemon connections.
# TYPE agentbox_host_connections gauge
agentbox_host_connections %d
# HELP agentbox_active_hosts Current connected hosts.
# TYPE agentbox_active_hosts gauge
agentbox_active_hosts %d
# HELP agentbox_active_boxes Current boxes with an active runtime.
# TYPE agentbox_active_boxes gauge
agentbox_active_boxes %s
# HELP agentbox_active_runs Current nonterminal runs inferred from box state.
# TYPE agentbox_active_runs gauge
agentbox_active_runs %s
# HELP agentbox_runtime_available Whether an enabled runtime is available on an online host.
# TYPE agentbox_runtime_available gauge
agentbox_runtime_available{runtime="omp"} %s
%s
# HELP agentbox_diagnostic_dependency_available Whether a metrics inventory dependency is available.
# TYPE agentbox_diagnostic_dependency_available gauge
agentbox_diagnostic_dependency_available{dependency="hosts"} %d
agentbox_diagnostic_dependency_available{dependency="boxes"} %d
# HELP agentbox_event_broadcast_dropped_total SSE subscribers disconnected due to backpressure.
# TYPE agentbox_event_broadcast_dropped_total counter
agentbox_event_broadcast_dropped_total %d
# HELP agentbox_events_ingested_total Persisted runtime events ingested.
# TYPE agentbox_events_ingested_total counter
agentbox_events_ingested_total %d
# HELP agentbox_commands_dispatched_total Persisted commands offered to connected hosts.
# TYPE agentbox_commands_dispatched_total counter
agentbox_commands_dispatched_total %d
# HELP agentbox_command_outcomes_total Terminal command acknowledgements by outcome.
# TYPE agentbox_command_outcomes_total counter
agentbox_command_outcomes_total{outcome="completed"} %d
agentbox_command_outcomes_total{outcome="failed"} %d
agentbox_command_outcomes_total{outcome="cancelled"} %d
# HELP agentbox_approval_timeouts_total Approval timeout commands produced by reconciliation.
# TYPE agentbox_approval_timeouts_total counter
agentbox_approval_timeouts_total %d
# HELP agentbox_schedule_executions_total Schedule executions dispatched to hosts.
# TYPE agentbox_schedule_executions_total counter
agentbox_schedule_executions_total{outcome="dispatched"} %d
# HELP agentbox_reaper_errors_total Reconciliation worker database errors.
# TYPE agentbox_reaper_errors_total counter
agentbox_reaper_errors_total %d
`,
		m.httpRequests.Load(),
		m.httpStatusClasses[1].Load(),
		m.httpStatusClasses[2].Load(),
		m.httpStatusClasses[3].Load(),
		m.httpStatusClasses[4].Load(),
		m.httpStatusClasses[5].Load(),
		strconv.FormatFloat(seconds, 'g', -1, 64),
		m.sseConnections.Load(),
		m.hostConnections.Load(),
		m.hostConnections.Load(),
		activeBoxes,
		activeRuns,
		ompAvailable,
		optionalRuntimeMetrics,
		m.hostInventoryReady.Load(),
		m.boxInventoryReady.Load(),
		m.eventBroadcastDropped.Load(),
		m.eventsIngested.Load(),
		m.commandsDispatched.Load(),
		m.commandOutcomes[0].Load(),
		m.commandOutcomes[1].Load(),
		m.commandOutcomes[2].Load(),
		m.approvalTimeouts.Load(),
		m.scheduleExecutions.Load(),
		m.reaperErrors.Load(),
	)
}

func metricGaugeValue(available bool, value int64) string {
	if !available {
		return "NaN"
	}
	return strconv.FormatInt(value, 10)
}

type diagnosticComponent struct {
	Ready   bool   `json:"ready"`
	Summary string `json:"summary,omitempty"`
}

type diagnosticRuntime struct {
	Enabled   bool `json:"enabled"`
	Available bool `json:"available"`
}

type diagnosticCounts struct {
	Hosts            *int `json:"hosts"`
	ConnectedHosts   int  `json:"connectedHosts"`
	Boxes            *int `json:"boxes"`
	ActiveBoxes      *int `json:"activeBoxes"`
	ActiveRuns       *int `json:"activeRuns"`
	PendingApprovals *int `json:"pendingApprovals"`
	Schedules        *int `json:"schedules"`
	ActiveSchedules  *int `json:"activeSchedules"`
}

type serverDiagnosticSnapshot struct {
	Status      string                         `json:"status"`
	GeneratedAt time.Time                      `json:"generatedAt"`
	Versions    map[string]string              `json:"versions"`
	Readiness   map[string]diagnosticComponent `json:"readiness"`
	Counts      diagnosticCounts               `json:"counts"`
	Runtimes    map[string]diagnosticRuntime   `json:"runtimes"`
	Failures    []diagnosticFailure            `json:"lastFailures"`
}

func (s *Server) handleDiagnostics(writer http.ResponseWriter, request *http.Request) {
	ctx, cancel := context.WithTimeout(request.Context(), 2*time.Second)
	defer cancel()
	writer.Header().Set("Cache-Control", "no-store")
	writeJSON(writer, http.StatusOK, s.diagnosticSnapshot(ctx))
}

func (s *Server) diagnosticSnapshot(ctx context.Context) serverDiagnosticSnapshot {
	hosts, hostErr := s.store.ListHosts(ctx, s.developmentUser)
	boxes, boxErr := s.store.ListBoxes(ctx, s.developmentUser)
	approvals, approvalErr := s.store.ListApprovals(ctx, s.developmentUser)
	schedules, scheduleErr := s.store.ListSchedules(ctx, s.developmentUser)

	s.updateHostGauges(hosts, hostErr)
	s.updateBoxGauges(boxes, boxErr)
	readiness := map[string]diagnosticComponent{
		"hosts":     diagnosticReadiness(hostErr, "host inventory is unavailable"),
		"boxes":     diagnosticReadiness(boxErr, "box inventory is unavailable"),
		"approvals": diagnosticReadiness(approvalErr, "approval inventory is unavailable"),
		"schedules": diagnosticReadiness(scheduleErr, "schedule inventory is unavailable"),
	}
	databaseReady := hostErr == nil && boxErr == nil && approvalErr == nil && scheduleErr == nil
	readiness["database"] = diagnosticComponent{Ready: databaseReady}
	if !databaseReady {
		readiness["database"] = diagnosticComponent{Ready: false, Summary: "one or more database inventories are unavailable"}
	}
	runtimeReady := hostErr == nil && (s.metrics.ompAvailable.Load() == 1 ||
		(s.enableCodex && s.metrics.codexAvailable.Load() == 1) ||
		(s.enableClaude && s.metrics.claudeAvailable.Load() == 1))
	readiness["runtime"] = diagnosticComponent{Ready: runtimeReady}
	if !runtimeReady {
		readiness["runtime"] = diagnosticComponent{Ready: false, Summary: "no online host exposes an enabled runtime"}
	}

	counts := diagnosticCounts{ConnectedHosts: int(s.metrics.hostConnections.Load())}
	if hostErr == nil {
		counts.Hosts = intPointer(len(hosts))
	}
	if boxErr == nil {
		activeBoxes, activeRuns := countActiveBoxesAndRuns(boxes)
		counts.Boxes = intPointer(len(boxes))
		counts.ActiveBoxes = intPointer(activeBoxes)
		counts.ActiveRuns = intPointer(activeRuns)
	}
	if approvalErr == nil {
		pending := 0
		for _, approval := range approvals {
			if approval.Status == "pending" {
				pending++
			}
		}
		counts.PendingApprovals = intPointer(pending)
	}
	if scheduleErr == nil {
		active := 0
		for _, schedule := range schedules {
			if schedule.Status == "active" {
				active++
			}
		}
		counts.Schedules = intPointer(len(schedules))
		counts.ActiveSchedules = intPointer(active)
	}

	status := "ready"
	if !databaseReady || !runtimeReady {
		status = "degraded"
	}
	runtimes := map[string]diagnosticRuntime{
		"omp": {Enabled: true, Available: hostErr == nil && s.metrics.ompAvailable.Load() == 1},
	}
	if s.enableCodex {
		runtimes["codex"] = diagnosticRuntime{Enabled: true, Available: hostErr == nil && s.metrics.codexAvailable.Load() == 1}
	}
	if s.enableClaude {
		runtimes["claude"] = diagnosticRuntime{Enabled: true, Available: hostErr == nil && s.metrics.claudeAvailable.Load() == 1}
	}
	return serverDiagnosticSnapshot{
		Status:      status,
		GeneratedAt: time.Now().UTC(),
		Versions: map[string]string{
			"server":        s.serverVersion,
			"api":           defaultAPIVersion,
			"minimumDaemon": "0.1.0",
		},
		Readiness: readiness,
		Counts:    counts,
		Runtimes:  runtimes,
		Failures:  s.metrics.failureSnapshot(),
	}
}

func (s *Server) refreshOperationalGauges(ctx context.Context) {
	hosts, hostErr := s.store.ListHosts(ctx, s.developmentUser)
	boxes, boxErr := s.store.ListBoxes(ctx, s.developmentUser)
	s.updateHostGauges(hosts, hostErr)
	s.updateBoxGauges(boxes, boxErr)
}

func (s *Server) updateHostGauges(hosts []domain.Host, err error) {
	if err != nil {
		s.metrics.hostInventoryReady.Store(0)
		s.metrics.recordFailure("host_inventory", "host inventory is unavailable")
		return
	}
	ompAvailable := false
	codexAvailable := false
	claudeAvailable := false
	for _, host := range hosts {
		if host.Status != domain.HostOnline || !s.hosts.connected(host.ID) {
			continue
		}
		for _, runtimeName := range host.Runtimes {
			switch runtimeName {
			case "omp":
				ompAvailable = true
			case "codex":
				codexAvailable = true
			case "claude":
				claudeAvailable = true
			}
		}
	}
	s.metrics.ompAvailable.Store(boolGauge(ompAvailable))
	s.metrics.codexAvailable.Store(boolGauge(codexAvailable))
	s.metrics.claudeAvailable.Store(boolGauge(claudeAvailable))
	s.metrics.hostInventoryReady.Store(1)
}

func (s *Server) updateBoxGauges(boxes []domain.Box, err error) {
	if err != nil {
		s.metrics.boxInventoryReady.Store(0)
		s.metrics.recordFailure("box_inventory", "box inventory is unavailable")
		return
	}
	activeBoxes, activeRuns := countActiveBoxesAndRuns(boxes)
	s.metrics.activeBoxes.Store(int64(activeBoxes))
	s.metrics.activeRuns.Store(int64(activeRuns))
	s.metrics.boxInventoryReady.Store(1)
}

func countActiveBoxesAndRuns(boxes []domain.Box) (int, int) {
	activeBoxes := 0
	activeRuns := 0
	for _, box := range boxes {
		switch box.Status {
		case domain.BoxStarting, domain.BoxIdle, domain.BoxRunning, domain.BoxWaitingApproval, domain.BoxHibernating:
			activeBoxes++
		}
		switch box.Status {
		case domain.BoxStarting, domain.BoxRunning, domain.BoxWaitingApproval:
			activeRuns++
		}
	}
	return activeBoxes, activeRuns
}

func diagnosticReadiness(err error, summary string) diagnosticComponent {
	if err == nil {
		return diagnosticComponent{Ready: true}
	}
	return diagnosticComponent{Ready: false, Summary: summary}
}

func boolGauge(value bool) int64 {
	if value {
		return 1
	}
	return 0
}

func intPointer(value int) *int {
	return &value
}

func (s *Server) observeScheduleDispatches(commands []domain.HostCommand) {
	var count uint64
	for _, command := range commands {
		if command.CommandType == "runtime.start" {
			count++
		}
	}
	if count > 0 {
		s.metrics.scheduleExecutions.Add(count)
	}
}

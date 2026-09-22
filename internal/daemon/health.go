package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	hostv1 "agentbox/api"
	hostclient "agentbox/internal/host"
)

type runtimeDiagnostic struct {
	Name      string `json:"name"`
	Version   string `json:"version,omitempty"`
	Available bool   `json:"available"`
	Summary   string `json:"summary,omitempty"`
}

type daemonFailure struct {
	Component string    `json:"component"`
	Summary   string    `json:"summary"`
	Observed  time.Time `json:"observedAt"`
}

type diagnosticComponent struct {
	Ready   bool   `json:"ready"`
	Summary string `json:"summary,omitempty"`
}

type HealthStatus struct {
	mu sync.RWMutex

	startedAt        time.Time
	connected        bool
	hostID           string
	runtimeAvailable bool
	runtimeVersion   string
	daemonVersion    string
	lastError        string
	manager          *Manager
	journal          *hostclient.Journal
	runtimes         []runtimeDiagnostic
	failures         map[string]daemonFailure
	commandOutcomes  [3]atomic.Uint64
}

func NewHealthStatus(manager *Manager, runtimeAvailable bool, runtimeVersion string) *HealthStatus {
	return &HealthStatus{
		startedAt:        time.Now().UTC(),
		runtimeAvailable: runtimeAvailable,
		runtimeVersion:   runtimeVersion,
		manager:          manager,
		failures:         make(map[string]daemonFailure),
	}
}

func (s *HealthStatus) configureDiagnostics(daemonVersion string, journal *hostclient.Journal, runtimes []runtimeDiagnostic) {
	s.mu.Lock()
	s.daemonVersion = daemonVersion
	s.journal = journal
	s.runtimes = append([]runtimeDiagnostic(nil), runtimes...)
	sort.Slice(s.runtimes, func(left, right int) bool { return s.runtimes[left].Name < s.runtimes[right].Name })
	s.mu.Unlock()
}

func (s *HealthStatus) Connected(welcome *hostv1.Welcome) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.connected = true
	s.lastError = ""
	if welcome != nil {
		s.hostID = welcome.HostId
	}
	slog.Info("agentboxd connected to control plane", "host_id", s.hostID)
}

func (s *HealthStatus) Disconnected(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.connected = false
	if err != nil {
		summary := daemonFailureSummary(err)
		s.lastError = summary
		s.recordFailureLocked("server_connection", summary)
	}
	slog.Warn("agentboxd disconnected from control plane", "error", err)
}

func (s *HealthStatus) SetError(err error) {
	if err == nil {
		return
	}
	s.RecordFailure("daemon", daemonFailureSummary(err))
}

func (s *HealthStatus) RecordFailure(component, summary string) {
	s.mu.Lock()
	s.lastError = summary
	s.recordFailureLocked(component, summary)
	s.mu.Unlock()
}

func (s *HealthStatus) recordFailureLocked(component, summary string) {
	s.failures[component] = daemonFailure{
		Component: component,
		Summary:   summary,
		Observed:  time.Now().UTC(),
	}
}

func (s *HealthStatus) observeCommandResult(result hostclient.CommandResult, err error) {
	index := -1
	if err != nil {
		index = 1
	} else {
		switch result.Stage {
		case hostv1.CommandStage_COMMAND_STAGE_COMPLETED:
			index = 0
		case hostv1.CommandStage_COMMAND_STAGE_FAILED:
			index = 1
		case hostv1.CommandStage_COMMAND_STAGE_CANCELLED:
			index = 2
		}
	}
	if index >= 0 {
		s.commandOutcomes[index].Add(1)
		if index == 1 {
			s.RecordFailure("command", "command execution failed")
		}
	}
}

type observedCommandHandler struct {
	manager *Manager
	health  *HealthStatus
}

func (h observedCommandHandler) HandleCommand(ctx context.Context, command *hostv1.HostCommand) (hostclient.CommandResult, error) {
	result, err := h.manager.HandleCommand(ctx, command)
	h.health.observeCommandResult(result, err)
	return result, err
}

func (s *HealthStatus) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.serveLiveness)
	mux.HandleFunc("GET /readyz", s.serveReadiness)
	mux.HandleFunc("GET /metrics", s.serveMetrics)
	mux.HandleFunc("GET /diagnostics", s.serveDiagnostics)
	return mux
}

func (s *HealthStatus) serveLiveness(writer http.ResponseWriter, _ *http.Request) {
	s.writeStatus(writer, http.StatusOK, "alive")
}

func (s *HealthStatus) serveReadiness(writer http.ResponseWriter, _ *http.Request) {
	s.mu.RLock()
	ready := s.connected && s.runtimeAvailable
	s.mu.RUnlock()
	if !ready {
		s.writeStatus(writer, http.StatusServiceUnavailable, "not_ready")
		return
	}
	s.writeStatus(writer, http.StatusOK, "ready")
}

func (s *HealthStatus) writeStatus(writer http.ResponseWriter, statusCode int, status string) {
	s.mu.RLock()
	response := struct {
		Status           string    `json:"status"`
		StartedAt        time.Time `json:"startedAt"`
		Connected        bool      `json:"connected"`
		HostID           string    `json:"hostId,omitempty"`
		RuntimeAvailable bool      `json:"runtimeAvailable"`
		RuntimeVersion   string    `json:"runtimeVersion,omitempty"`
		ActiveBoxes      int       `json:"activeBoxes"`
		LastError        string    `json:"lastError,omitempty"`
	}{
		Status:           status,
		StartedAt:        s.startedAt,
		Connected:        s.connected,
		HostID:           s.hostID,
		RuntimeAvailable: s.runtimeAvailable,
		RuntimeVersion:   s.runtimeVersion,
		ActiveBoxes:      s.manager.ActiveBoxes(),
		LastError:        s.lastError,
	}
	s.mu.RUnlock()

	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(statusCode)
	_ = json.NewEncoder(writer).Encode(response)
}

type daemonDiagnosticCounts struct {
	ActiveBoxes           int    `json:"activeBoxes"`
	ActiveRuns            int    `json:"activeRuns"`
	JournalBacklogRecords *int   `json:"journalBacklogRecords"`
	JournalBacklogBytes   *int64 `json:"journalBacklogBytes"`
}

type daemonDiagnosticVersions struct {
	Daemon   string            `json:"daemon"`
	Runtimes map[string]string `json:"runtimes"`
}

type daemonDiagnosticSnapshot struct {
	Status       string                         `json:"status"`
	GeneratedAt  time.Time                      `json:"generatedAt"`
	StartedAt    time.Time                      `json:"startedAt"`
	Versions     daemonDiagnosticVersions       `json:"versions"`
	Readiness    map[string]diagnosticComponent `json:"readiness"`
	Counts       daemonDiagnosticCounts         `json:"counts"`
	Runtimes     []runtimeDiagnostic            `json:"runtimes"`
	LastFailures []daemonFailure                `json:"lastFailures"`
}

func (s *HealthStatus) serveDiagnostics(writer http.ResponseWriter, _ *http.Request) {
	s.mu.RLock()
	connected := s.connected
	runtimeAvailable := s.runtimeAvailable
	daemonVersion := s.daemonVersion
	startedAt := s.startedAt
	journal := s.journal
	runtimes := append([]runtimeDiagnostic(nil), s.runtimes...)
	failures := make([]daemonFailure, 0, len(s.failures))
	for _, failure := range s.failures {
		failures = append(failures, failure)
	}
	s.mu.RUnlock()
	sort.Slice(failures, func(left, right int) bool { return failures[left].Component < failures[right].Component })

	activeBoxes, activeRuns, shuttingDown := s.manager.OperationalCounts()
	readiness := map[string]diagnosticComponent{
		"serverConnection": {Ready: connected},
		"runtime":          {Ready: runtimeAvailable},
		"manager":          {Ready: !shuttingDown},
	}
	if !connected {
		readiness["serverConnection"] = diagnosticComponent{Ready: false, Summary: "server connection is unavailable"}
	}
	if !runtimeAvailable {
		readiness["runtime"] = diagnosticComponent{Ready: false, Summary: "no enabled runtime is available"}
	}
	if shuttingDown {
		readiness["manager"] = diagnosticComponent{Ready: false, Summary: "runtime manager is shutting down"}
	}
	counts := daemonDiagnosticCounts{ActiveBoxes: activeBoxes, ActiveRuns: activeRuns}
	journalReady := journal != nil
	if journal != nil {
		stats := journal.Stats()
		journalReady = !stats.Closed
		records := stats.Records
		bytes := stats.Bytes
		counts.JournalBacklogRecords = &records
		counts.JournalBacklogBytes = &bytes
	}
	readiness["journal"] = diagnosticComponent{Ready: journalReady}
	if !journalReady {
		readiness["journal"] = diagnosticComponent{Ready: false, Summary: "outbound journal is unavailable"}
	}

	versions := make(map[string]string, len(runtimes))
	for _, runtimeStatus := range runtimes {
		if runtimeStatus.Version != "" {
			versions[runtimeStatus.Name] = runtimeStatus.Version
		}
	}
	status := "ready"
	if !connected || !runtimeAvailable || shuttingDown || !journalReady {
		status = "degraded"
	}
	response := daemonDiagnosticSnapshot{
		Status:      status,
		GeneratedAt: time.Now().UTC(),
		StartedAt:   startedAt,
		Versions: daemonDiagnosticVersions{
			Daemon:   daemonVersion,
			Runtimes: versions,
		},
		Readiness:    readiness,
		Counts:       counts,
		Runtimes:     runtimes,
		LastFailures: failures,
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(writer).Encode(response)
}

func (s *HealthStatus) serveMetrics(writer http.ResponseWriter, _ *http.Request) {
	s.mu.RLock()
	connected := s.connected
	journal := s.journal
	runtimes := append([]runtimeDiagnostic(nil), s.runtimes...)
	s.mu.RUnlock()
	activeBoxes, activeRuns, _ := s.manager.OperationalCounts()
	journalReady := false
	journalRecords := "NaN"
	journalBytes := "NaN"
	if journal != nil {
		stats := journal.Stats()
		journalReady = !stats.Closed
		journalRecords = strconv.Itoa(stats.Records)
		journalBytes = strconv.FormatInt(stats.Bytes, 10)
	}
	writer.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(writer, `# HELP agentboxd_connected Whether the daemon is connected to the control plane.
# TYPE agentboxd_connected gauge
agentboxd_connected %d
# HELP agentboxd_active_boxes Current active runtime boxes.
# TYPE agentboxd_active_boxes gauge
agentboxd_active_boxes %d
# HELP agentboxd_active_runs Current runs with an armed execution deadline.
# TYPE agentboxd_active_runs gauge
agentboxd_active_runs %d
# HELP agentboxd_command_outcomes_total Commands completed by terminal outcome.
# TYPE agentboxd_command_outcomes_total counter
agentboxd_command_outcomes_total{outcome="completed"} %d
agentboxd_command_outcomes_total{outcome="failed"} %d
agentboxd_command_outcomes_total{outcome="cancelled"} %d
# HELP agentboxd_journal_available Whether the outbound journal is available.
# TYPE agentboxd_journal_available gauge
agentboxd_journal_available %d
# HELP agentboxd_journal_backlog_records Current unacknowledged outbound journal records.
# TYPE agentboxd_journal_backlog_records gauge
agentboxd_journal_backlog_records %s
# HELP agentboxd_journal_backlog_bytes Current outbound journal bytes on disk.
# TYPE agentboxd_journal_backlog_bytes gauge
agentboxd_journal_backlog_bytes %s
`,
		boolMetric(connected),
		activeBoxes,
		activeRuns,
		s.commandOutcomes[0].Load(),
		s.commandOutcomes[1].Load(),
		s.commandOutcomes[2].Load(),
		boolMetric(journalReady),
		journalRecords,
		journalBytes,
	)
	_, _ = fmt.Fprint(writer, "# HELP agentboxd_runtime_available Whether a configured runtime passed its startup probe.\n")
	_, _ = fmt.Fprint(writer, "# TYPE agentboxd_runtime_available gauge\n")
	for _, runtimeStatus := range runtimes {
		_, _ = fmt.Fprintf(writer, "agentboxd_runtime_available{runtime=\"%s\"} %d\n",
			prometheusLabelValue(runtimeStatus.Name), boolMetric(runtimeStatus.Available))
	}
}

func daemonFailureSummary(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "operation timed out"
	case errors.Is(err, context.Canceled):
		return "operation cancelled"
	}
	var protocolErr *hostclient.ProtocolError
	if errors.As(err, &protocolErr) {
		return "host " + string(protocolErr.Code) + " failure"
	}
	return "operation failed"
}

func prometheusLabelValue(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "\n", "\\n")
	return strings.ReplaceAll(value, "\"", "\\\"")
}

func boolMetric(value bool) int {
	if value {
		return 1
	}
	return 0
}

var _ interface {
	Connected(*hostv1.Welcome)
	Disconnected(error)
} = (*HealthStatus)(nil)

var _ hostclient.CommandHandler = observedCommandHandler{}

package daemon

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	hostv1 "agentbox/api"
)

type HealthStatus struct {
	mu sync.RWMutex

	startedAt        time.Time
	connected        bool
	hostID           string
	runtimeAvailable bool
	runtimeVersion   string
	lastError        string
	manager          *Manager
}

func NewHealthStatus(manager *Manager, runtimeAvailable bool, runtimeVersion string) *HealthStatus {
	return &HealthStatus{
		startedAt:        time.Now().UTC(),
		runtimeAvailable: runtimeAvailable,
		runtimeVersion:   runtimeVersion,
		manager:          manager,
	}
}

func (s *HealthStatus) Connected(welcome *hostv1.Welcome) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.connected = true
	s.lastError = ""
	if welcome != nil {
		s.hostID = welcome.HostId
	}
}

func (s *HealthStatus) Disconnected(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.connected = false
	if err != nil {
		s.lastError = err.Error()
	}
}

func (s *HealthStatus) SetError(err error) {
	if err == nil {
		return
	}
	s.mu.Lock()
	s.lastError = err.Error()
	s.mu.Unlock()
}

func (s *HealthStatus) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.serveLiveness)
	mux.HandleFunc("GET /readyz", s.serveReadiness)
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

var _ interface {
	Connected(*hostv1.Welcome)
	Disconnected(error)
} = (*HealthStatus)(nil)

package observability

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// HealthProbe is a named function that checks a single component.
// A nil error from Check means the component is healthy.
type HealthProbe struct {
	Name  string
	Check func() error // nil error = healthy
}

// HealthStatus is the JSON response body returned for unhealthy checks.
type HealthStatus struct {
	Healthy bool              `json:"healthy"`
	Checks  map[string]string `json:"checks"` // name -> error string; empty if healthy
}

// HealthHandler is an http.Handler for the /healthz endpoint.
// It runs all registered probes and returns 200 (body "ok") when all pass,
// or 503 with a diagnostic JSON body listing the failing probe names and errors.
type HealthHandler struct {
	probes []HealthProbe
}

// NewHealthHandler creates a HealthHandler with the given probes.
// Pass nil or an empty slice to get a permanently-healthy handler.
func NewHealthHandler(probes []HealthProbe) *HealthHandler {
	return &HealthHandler{probes: probes}
}

// ServeHTTP implements http.Handler. It runs all probes and returns:
//   - 200 with body "ok" when no probes are failing
//   - 503 with Content-Type application/json and a HealthStatus body when any probe fails
func (h *HealthHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	failing := make(map[string]string)
	for _, p := range h.probes {
		if err := p.Check(); err != nil {
			failing[p.Name] = err.Error()
		}
	}
	if len(failing) == 0 {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusServiceUnavailable)
	json.NewEncoder(w).Encode(HealthStatus{Healthy: false, Checks: failing})
}

package httpapi

import (
	"context"
	"net/http"
	"time"
)

// healthProbeTimeout bounds the database check so a wedged pool cannot make the
// probe itself hang; an orchestrator's own timeout should be longer than this.
const healthProbeTimeout = 2 * time.Second

// healthResponse is the body of a successful /healthz probe.
type healthResponse struct {
	Status   string `json:"status"`
	Database string `json:"database"`
	Version  string `json:"version,omitempty"`
}

// handleHealth reports whether the process can serve traffic. It is a readiness
// probe, not a liveness probe: it fails when the database is unreachable, which
// is exactly when the instance should be pulled out of rotation.
func (s *server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")

	ctx, cancel := context.WithTimeout(r.Context(), healthProbeTimeout)
	defer cancel()

	if err := s.opts.DB.Ping(ctx); err != nil {
		LoggerFrom(r.Context()).WarnContext(r.Context(), "health probe failed", "error", err)
		WriteError(w, r, http.StatusServiceUnavailable, CodeUnavailable,
			"The database is unreachable.")
		return
	}

	WriteJSON(w, r, http.StatusOK, healthResponse{
		Status:   "ok",
		Database: "ok",
		Version:  s.opts.Version,
	})
}

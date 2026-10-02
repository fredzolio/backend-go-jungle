// Package health serves liveness and readiness. Readiness aggregates dependency
// probes contributed by other components and flips to DOWN while draining.
package health

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"
)

// Check is one named readiness probe.
type Check struct {
	Probe func(ctx context.Context) error
	Name  string
}

// Registry owns the readiness state of the process.
type Registry struct {
	log      *slog.Logger
	checks   []Check
	timeout  time.Duration
	draining atomic.Bool
}

// NewRegistry builds a registry over the given probes.
func NewRegistry(log *slog.Logger, checks []Check) *Registry {
	return &Registry{log: log, checks: checks, timeout: 2 * time.Second}
}

// StartDraining makes readiness report DOWN from now on.
func (r *Registry) StartDraining() { r.draining.Store(true) }

type report struct {
	Checks map[string]string `json:"checks,omitempty"`
	Status string            `json:"status"`
}

const (
	statusUp   = "UP"
	statusDown = "DOWN"
)

// Live reports that the process is running.
func (r *Registry) Live(w http.ResponseWriter, _ *http.Request) {
	write(w, http.StatusOK, report{Status: statusUp})
}

// Ready reports whether every dependency answers within the probe timeout.
// Error details go to the log only: the endpoint is public.
func (r *Registry) Ready(w http.ResponseWriter, req *http.Request) {
	if r.draining.Load() {
		write(w, http.StatusServiceUnavailable, report{Status: statusDown})
		return
	}
	out := report{Status: statusUp, Checks: make(map[string]string, len(r.checks))}
	for _, c := range r.checks {
		ctx, cancel := context.WithTimeout(req.Context(), r.timeout)
		err := c.Probe(ctx)
		cancel()
		if err != nil {
			r.log.WarnContext(req.Context(), "readiness probe failed", slog.String("check", c.Name), slog.Any("error", err))
			out.Checks[c.Name] = statusDown
			out.Status = statusDown
			continue
		}
		out.Checks[c.Name] = statusUp
	}
	code := http.StatusOK
	if out.Status == statusDown {
		code = http.StatusServiceUnavailable
	}
	write(w, code, out)
}

func write(w http.ResponseWriter, code int, body report) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body) // client went away; nothing to recover
}

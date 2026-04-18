package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/JeiKeiLim/my-home-index-server/internal/model"
)

// scanBudget is the hard ceiling on how long the scanner+inspector
// pipeline may take per /ports request (Iron Law 6 — scan ≤ 1s).
const scanBudget = 1 * time.Second

// snapshotCache holds the most recent successful /ports build so a
// timeout can return last-known-good data instead of a blank UI. The
// snapshot is shared across goroutines; access is mutex-guarded.
type snapshotCache struct {
	mu   sync.RWMutex
	rows []model.PortVM
	at   time.Time
}

func (c *snapshotCache) Get() ([]model.PortVM, time.Time) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.rows, c.at
}

func (c *snapshotCache) Set(rows []model.PortVM) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rows = rows
	c.at = time.Now()
}

// buildSnapshot runs scanner.Scan → inspector.Inspect (parallel,
// bounded) → store.Label → BuildViewModels under the configured scan
// budget. Returns the rows on success, or an error (typically context
// deadline) on failure.
func (s *Server) buildSnapshot(parent context.Context) ([]model.PortVM, error) {
	if s.scanner == nil {
		return []model.PortVM{}, nil
	}
	ctx, cancel := context.WithTimeout(parent, scanBudget)
	defer cancel()

	listeners, err := s.scanner.Scan(ctx)
	if err != nil {
		return nil, err
	}
	// Pass an explicit nil interface when the store dep is missing —
	// otherwise BuildViewModels receives a typed-nil *store.Store and
	// panics on the method call.
	var lookup model.LabelLookup
	if s.store != nil {
		lookup = s.store
	}
	rows := model.BuildViewModels(ctx, listeners, s.inspector, lookup, s.selfPID)
	if err := ctx.Err(); err != nil {
		return rows, err
	}
	return rows, nil
}

// handlePortsHTML returns the dashboard fragment: <tr> rows for desktop
// AND a hx-swap-oob block of <div class="card"> entries for mobile, in
// one response. On scan timeout we return 200 with the last good
// snapshot and X-Scan-Error: timeout so the UI stays responsive.
func (s *Server) handlePortsHTML(w http.ResponseWriter, r *http.Request) {
	rows, err := s.buildSnapshot(r.Context())
	if err != nil && errors.Is(err, context.DeadlineExceeded) {
		cached, _ := s.snapshot.Get()
		rows = cached
		w.Header().Set("X-Scan-Error", "timeout")
	} else if err != nil {
		w.Header().Set("X-Scan-Error", "error")
		cached, _ := s.snapshot.Get()
		rows = cached
	} else {
		s.snapshot.Set(rows)
	}

	body, err := renderTemplate("_ports.html", rows)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(body)
}

// handlePortsJSON returns the same data as handlePortsHTML but as the
// machine-readable shape documented in the spec. Same timeout fallback
// as the HTML endpoint.
func (s *Server) handlePortsJSON(w http.ResponseWriter, r *http.Request) {
	rows, err := s.buildSnapshot(r.Context())
	if err != nil && errors.Is(err, context.DeadlineExceeded) {
		cached, _ := s.snapshot.Get()
		rows = cached
		w.Header().Set("X-Scan-Error", "timeout")
	} else if err != nil {
		cached, _ := s.snapshot.Get()
		rows = cached
		w.Header().Set("X-Scan-Error", "error")
	} else {
		s.snapshot.Set(rows)
	}

	if rows == nil {
		rows = []model.PortVM{}
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	enc := json.NewEncoder(w)
	_ = enc.Encode(rows)
}

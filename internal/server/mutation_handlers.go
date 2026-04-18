package server

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/JeiKeiLim/my-home-index-server/internal/model"
	"github.com/JeiKeiLim/my-home-index-server/internal/process"
	"github.com/JeiKeiLim/my-home-index-server/internal/store"
)

// mutationScanBudget caps scanner+inspector work done inside the
// mutation handlers. Keeps them inside the same 1s Iron Law window as
// the dashboard refresh.
const mutationScanBudget = 1 * time.Second

// handleKillMutation is the real POST /kill/{port} flow wired after
// job-8. It validates the port range (Iron Law 3), resolves the PID
// via a FRESH scanner.Scan (never trusting the request body for PID —
// Iron Law 1 self-preservation), snapshots env/argv/cwd via the
// inspector BEFORE the kill so the Remembered entry keeps the
// restart-time environment (Darwin KERN_PROCARGS2 stops working after
// the process exits), persists the Remembered entry, then dispatches
// the kill. On success it returns the updated /ports fragment so htmx
// swaps the table without an extra round-trip.
func (s *Server) handleKillMutation(w http.ResponseWriter, r *http.Request) {
	port, err := strconv.Atoi(r.PathValue("port"))
	if err != nil {
		http.Error(w, "bad port", http.StatusBadRequest)
		return
	}
	if !s.portInRange(port) {
		http.Error(w, "port out of range", http.StatusBadRequest)
		return
	}
	if s.process == nil {
		http.Error(w, "process manager not wired", http.StatusServiceUnavailable)
		return
	}
	if s.scanner == nil {
		http.Error(w, "scanner not wired", http.StatusServiceUnavailable)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), mutationScanBudget)
	defer cancel()

	pid, found, err := s.resolvePIDForPort(ctx, port)
	if err != nil {
		http.Error(w, "scan failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	if !found {
		http.Error(w, "no listener on port", http.StatusNotFound)
		return
	}
	// Iron Law 1 (self-preservation) — redundant with process.Kill's
	// own guard but asserted here so the handler layer fails loud
	// before any syscall runs.
	if pid == s.selfPID {
		http.Error(w, "refusing to kill self", http.StatusForbidden)
		return
	}

	// Snapshot the process env BEFORE the kill. Darwin's
	// KERN_PROCARGS2 returns nothing once the pid has exited, so this
	// has to happen while the listener is still live. The snapshot is
	// stashed in a local Remembered value but NOT persisted yet — if
	// the kill fails the listener is still alive and a remembered row
	// would be a phantom restart-pending entry the UI would surface.
	var (
		pending     store.Remembered
		havePending bool
	)
	if s.inspector != nil {
		if info, _ := s.inspector.Inspect(ctx, pid); info != nil {
			pending = store.Remembered{
				Port:    port,
				Command: info.Command,
				Cwd:     info.Cwd,
				Env:     info.Env,
			}
			havePending = true
		}
	}

	if err := s.process.Kill(ctx, process.Target{PID: pid, Port: port}); err != nil {
		if errors.Is(err, process.ErrSelfPID) {
			http.Error(w, "refusing to kill self", http.StatusForbidden)
			return
		}
		http.Error(w, "kill failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Only persist the remembered entry after a successful Kill — the
	// /ports fragment on the next poll will otherwise show a stale
	// restart-pending row for a still-live listener.
	if havePending && s.store != nil {
		_ = s.store.Remember(pending)
	}

	s.writePortsFragment(w, r)
}

// handleRestartMutation is POST /restart/{id}. The Remembered lookup
// lives in the store; process.Restart handles the SpawnDetached flow
// with the stored cwd/argv/env (Iron Law 4 — restart never takes
// caller-supplied cwd). Returns 202 on success.
func (s *Server) handleRestartMutation(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		http.Error(w, "missing remembered id", http.StatusBadRequest)
		return
	}
	if s.process == nil {
		http.Error(w, "process manager not wired", http.StatusServiceUnavailable)
		return
	}
	if s.store == nil {
		http.Error(w, "store not wired", http.StatusServiceUnavailable)
		return
	}

	// Bound the restart dispatch to the scan budget so a hung
	// SpawnDetached can't wedge the request goroutine past Iron Law 12's
	// write timeout.
	ctx, cancel := context.WithTimeout(r.Context(), mutationScanBudget)
	defer cancel()

	if err := s.process.Restart(ctx, id); err != nil {
		if errors.Is(err, process.ErrRememberedNotFound) {
			http.Error(w, "remembered entry not found", http.StatusNotFound)
			return
		}
		http.Error(w, "restart failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write([]byte("restart dispatched"))
}

// handleRenameMutation is POST /rename/{port}. Reads "label" from the
// form body, validates it per spec (≤64 chars, no control characters,
// otherwise any unicode allowed), resolves the current (cwd, command)
// for the port via a fresh scan+inspect, and upserts the label.
// Empty label clears the stored entry.
func (s *Server) handleRenameMutation(w http.ResponseWriter, r *http.Request) {
	port, err := strconv.Atoi(r.PathValue("port"))
	if err != nil {
		http.Error(w, "bad port", http.StatusBadRequest)
		return
	}
	if !s.portInRange(port) {
		http.Error(w, "port out of range", http.StatusBadRequest)
		return
	}
	if s.scanner == nil {
		http.Error(w, "scanner not wired", http.StatusServiceUnavailable)
		return
	}
	if s.inspector == nil {
		http.Error(w, "inspector not wired", http.StatusServiceUnavailable)
		return
	}
	if s.store == nil {
		http.Error(w, "store not wired", http.StatusServiceUnavailable)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	label := r.PostForm.Get("label")
	if err := validateLabel(label); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), mutationScanBudget)
	defer cancel()

	pid, found, err := s.resolvePIDForPort(ctx, port)
	if err != nil {
		http.Error(w, "scan failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	if !found {
		http.Error(w, "no listener on port", http.StatusNotFound)
		return
	}

	info, err := s.inspector.Inspect(ctx, pid)
	if err != nil || info == nil {
		http.Error(w, "inspect failed", http.StatusBadGateway)
		return
	}

	if err := s.store.SetLabel(info.Cwd, info.Command, label); err != nil {
		if errors.Is(err, store.ErrLabelTooLong) {
			http.Error(w, "label too long", http.StatusBadRequest)
			return
		}
		http.Error(w, "persist label: "+err.Error(), http.StatusInternalServerError)
		return
	}

	s.writePortsFragment(w, r)
}

// resolvePIDForPort runs a fresh scan and returns the PID of the
// listener bound to port. The second return value disambiguates "no
// listener found" (found=false, err=nil) from "scan failed" (err!=nil).
func (s *Server) resolvePIDForPort(ctx context.Context, port int) (int, bool, error) {
	listeners, err := s.scanner.Scan(ctx)
	if err != nil {
		return 0, false, err
	}
	for _, l := range listeners {
		if l.Port == port {
			return l.PID, true, nil
		}
	}
	return 0, false, nil
}

// writePortsFragment re-runs the snapshot pipeline and writes the same
// HTML the polling GET /ports would produce. Kept separate from
// handlePortsHTML so the mutation handlers can share the render while
// still owning their own error response codes. Distinguishes
// context-deadline ("timeout") from other scanner errors ("error") the
// same way handlePortsHTML does.
func (s *Server) writePortsFragment(w http.ResponseWriter, r *http.Request) {
	rows, err := s.buildSnapshot(r.Context())
	if err != nil {
		// On a scan failure after a successful mutation we still want
		// the client to rerender, so fall back to the cached snapshot
		// rather than 500ing.
		cached, _ := s.snapshot.Get()
		rows = cached
		if errors.Is(err, context.DeadlineExceeded) {
			w.Header().Set("X-Scan-Error", "timeout")
		} else {
			w.Header().Set("X-Scan-Error", "error")
		}
	} else {
		s.snapshot.Set(rows)
	}
	body, renderErr := renderTemplate("_ports.html", rows)
	if renderErr != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(body)
}

// handleRememberedList renders the "restart history" view as the same
// /ports fragment shape (desktop <tr> rows + mobile cards). Each row
// has Remembered=true so the template shows it with the
// `restart-pending` class + data-restart attribute that the
// dashboard's existing restart button picks up.
func (s *Server) handleRememberedList(w http.ResponseWriter, _ *http.Request) {
	if s.store == nil {
		http.Error(w, "store not wired", http.StatusServiceUnavailable)
		return
	}
	entries, err := s.store.AllRemembered()
	if err != nil {
		http.Error(w, "list remembered: "+err.Error(), http.StatusInternalServerError)
		return
	}
	rows := make([]model.PortVM, 0, len(entries))
	for _, e := range entries {
		rows = append(rows, model.PortVM{
			Port:         e.Port,
			Cmd:          strings.Join(e.Command, " "),
			Cwd:          e.Cwd,
			Source:       "remembered",
			Remembered:   true,
			RememberedID: e.ID,
			Alive:        false,
		})
	}
	body, err := renderTemplate("_ports.html", rows)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(body)
}

// validateLabel enforces the spec's label invariants (≤64 chars, no
// control characters, any other unicode allowed).
func validateLabel(label string) error {
	if label == "" {
		return nil
	}
	if utf8.RuneCountInString(label) > store.MaxLabelLen {
		return errors.New("label exceeds 64 characters")
	}
	for _, r := range label {
		if unicode.IsControl(r) {
			return errors.New("label contains a control character")
		}
	}
	return nil
}

// Package model flattens scanner+inspector+store data into the per-row
// view-model the dashboard template (and /ports.json) consume. Keeping
// the assembly out of the server package means the HTTP handler stays
// thin and the composition is easy to unit-test without httptest.
package model

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/JeiKeiLim/my-home-index-server/internal/inspector"
	"github.com/JeiKeiLim/my-home-index-server/internal/scanner"
)

// PortVM is one row in the dashboard. JSON tags drive /ports.json; the
// HTML template reads the same fields so the two outputs cannot drift.
type PortVM struct {
	Port         int      `json:"port"`
	Label        string   `json:"label"`
	Cmd          string   `json:"cmd"`
	Cwd          string   `json:"cwd"`
	UptimeS      int64    `json:"uptime_s"`
	Source       string   `json:"source"`
	PID          int      `json:"pid"`
	ListenAddrs  []string `json:"listen_addrs"`
	Alive        bool     `json:"alive"`
	Remembered   bool     `json:"remembered"`
	RememberedID string   `json:"remembered_id,omitempty"`

	// Display-only fields (not serialised to JSON).
	Uptime   string `json:"-"`
	Self     bool   `json:"-"`
	Disabled bool   `json:"-"`
}

// LabelLookup is the minimal store interface BuildViewModels needs. Any
// type with `Label(cwd, command)` satisfies it; the real *store.Store
// does, and tests pass a fake without dragging in the JSON file path.
type LabelLookup interface {
	Label(cwd string, command []string) (string, error)
}

// MaxParallelInspect bounds inspector concurrency. The 1s scan budget
// (Iron Law 6) leaves little room for serial inspects when many PIDs
// are listening; 8 saturates the kqueue backend without flooding.
const MaxParallelInspect = 8

// BuildViewModels turns raw listeners into render-ready rows.
//
// Pipeline per listener:
//  1. Skip selfPID (defence-in-depth — the scanner already excludes us).
//  2. inspector.Inspect (best-effort; missing fields stay zero).
//  3. store.Label lookup keyed on the inspected (cwd, argv).
//
// Inspects run in parallel, capped at MaxParallelInspect via a buffered
// semaphore so the handler's ctx.Deadline still gates the whole batch.
// Output is sorted by port for stable rendering.
func BuildViewModels(
	ctx context.Context,
	listeners []scanner.Listener,
	insp inspector.Inspector,
	st LabelLookup,
	selfPID int,
) []PortVM {
	if len(listeners) == 0 {
		return []PortVM{}
	}
	now := time.Now()

	sem := make(chan struct{}, MaxParallelInspect)
	results := make([]PortVM, len(listeners))
	var wg sync.WaitGroup
	for i, l := range listeners {
		if l.PID == selfPID {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(idx int, l scanner.Listener) {
			defer wg.Done()
			defer func() { <-sem }()
			results[idx] = buildOne(ctx, l, insp, st, now)
		}(i, l)
	}
	wg.Wait()

	out := make([]PortVM, 0, len(results))
	for _, vm := range results {
		if vm.Port == 0 && vm.PID == 0 {
			// Skipped (selfPID) or zero-valued — discard.
			continue
		}
		out = append(out, vm)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Port < out[j].Port })
	return out
}

func buildOne(
	ctx context.Context,
	l scanner.Listener,
	insp inspector.Inspector,
	st LabelLookup,
	now time.Time,
) PortVM {
	vm := PortVM{
		Port:        l.Port,
		PID:         l.PID,
		ListenAddrs: append([]string(nil), l.Addrs...),
		Alive:       true,
		Source:      "external",
	}
	if insp == nil {
		return vm
	}
	info, err := insp.Inspect(ctx, l.PID)
	if err != nil || info == nil {
		// Process disappeared between scan and inspect. The row still
		// shows the port + pid; cmd/cwd stay empty.
		return vm
	}
	vm.Cmd = strings.Join(info.Command, " ")
	vm.Cwd = info.Cwd
	if !info.StartTime.IsZero() {
		dur := now.Sub(info.StartTime)
		if dur < 0 {
			dur = 0
		}
		vm.UptimeS = int64(dur.Seconds())
		vm.Uptime = FormatUptime(dur)
	}
	if st != nil {
		if label, lerr := st.Label(info.Cwd, info.Command); lerr == nil {
			vm.Label = label
		}
	}
	return vm
}

// FormatUptime renders a duration as the dashboard's compact uptime
// string ("9m", "2h 18m", "1d 3h"). Exposed so the handler/template can
// reuse the same formatting if it wants to re-render server-side.
func FormatUptime(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	s := int(d.Seconds())
	if s < 60 {
		return fmt.Sprintf("%ds", s)
	}
	m := s / 60
	if m < 60 {
		return fmt.Sprintf("%dm", m)
	}
	h := m / 60
	if h < 24 {
		return fmt.Sprintf("%dh %02dm", h, m%60)
	}
	days := h / 24
	return fmt.Sprintf("%dd %dh", days, h%24)
}

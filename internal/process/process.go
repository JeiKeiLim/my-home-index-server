// Package process orchestrates the three privileged operations the
// dashboard offers — Kill, Restart, and SpawnDetached — while honoring
// the Iron Laws in .tenet/harness/current.md:
//
//   - #1 Self-preservation (two guards — PID check at Kill entry, plus
//     a scanner re-check so a same-millisecond race cannot smuggle in
//     our own PID).
//   - #5 No shell interpolation (os/exec.Command with argv slice).
//   - #9 Detached children run in their own session and process group
//     so SIGHUP from the dashboard's pgid can never cascade into them.
//
// Kill returns within ~50ms: the SIGTERM goes out synchronously, then a
// goroutine waits for the grace window and escalates to SIGKILL if the
// target still exists. Spawned children get a reaper goroutine so that
// short-lived restarts (e.g. `true`) do not accumulate as zombies.
package process

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/JeiKeiLim/my-home-index-server/internal/config"
	"github.com/JeiKeiLim/my-home-index-server/internal/scanner"
	"github.com/JeiKeiLim/my-home-index-server/internal/store"
)

// Config re-exports config.Config so callers do not need to double-import.
type Config = config.Config

// Target identifies a live TCP listener to act on.
type Target struct {
	PID  int
	Port int
}

// Spec describes a detached child to spawn.
type Spec struct {
	Command []string
	Cwd     string
	Env     []string
}

// ErrSelfPID is returned when Kill is called on the dashboard's own PID.
var ErrSelfPID = errors.New("process: refusing to act on self PID")

// ErrUnknownPID is returned when the scanner re-check cannot find the
// target PID among live same-uid listeners (covers cross-uid, dead, or
// out-of-range PIDs).
var ErrUnknownPID = errors.New("process: PID not found among live listeners")

// ErrEmptyCommand is returned when Spec.Command has no argv[0].
var ErrEmptyCommand = errors.New("process: spec.Command must contain argv[0]")

// ErrStoreRequired is returned when Restart is called without a store
// attached via WithStore.
var ErrStoreRequired = errors.New("process: store not attached — cannot Restart")

// ErrRememberedNotFound is returned when Restart is called with an id
// the store does not know.
var ErrRememberedNotFound = errors.New("process: remembered entry not found")

// Option customises a Manager at construction time.
type Option func(*Manager)

// WithStore attaches a store used by Restart to look up remembered entries.
func WithStore(s *store.Store) Option { return func(m *Manager) { m.store = s } }

// WithScannerFactory overrides the scanner constructor used for the
// second-layer self-PID / cross-uid guard. Tests use this to inject a
// deterministic scanner.
func WithScannerFactory(f func() (scanner.Scanner, error)) Option {
	return func(m *Manager) { m.newScanner = f }
}

// WithKillReturnBudget overrides the soft budget Kill must fit inside.
// Defaults to 50ms; tests that want to exercise timing bounds without a
// real sleep can shorten it.
func WithKillReturnBudget(d time.Duration) Option {
	return func(m *Manager) { m.killReturnBudget = d }
}

// Manager is the public surface described in decomposition §process (job-6).
// It is safe for concurrent use; all state is immutable after New.
type Manager struct {
	cfg              *Config
	grace            time.Duration
	killReturnBudget time.Duration
	store            *store.Store
	newScanner       func() (scanner.Scanner, error)
	selfPID          int
}

// New builds a Manager bound to cfg. KillGraceMS translates to the
// SIGTERM→SIGKILL window; values ≤ 0 fall back to the 3s default so a
// zero-valued Config does not silently disable escalation.
func New(cfg *Config, opts ...Option) (*Manager, error) {
	if cfg == nil {
		return nil, errors.New("process: nil config")
	}
	grace := time.Duration(cfg.KillGraceMS) * time.Millisecond
	if grace <= 0 {
		grace = 3 * time.Second
	}
	m := &Manager{
		cfg:              cfg,
		grace:            grace,
		killReturnBudget: 50 * time.Millisecond,
		selfPID:          os.Getpid(),
		newScanner: func() (scanner.Scanner, error) {
			return scanner.Auto(cfg)
		},
	}
	for _, opt := range opts {
		opt(m)
	}
	return m, nil
}

// KillGrace reports the configured SIGTERM→SIGKILL window.
func (m *Manager) KillGrace() time.Duration { return m.grace }

// Kill sends SIGTERM to t.PID and returns within the configured return
// budget (default 50ms). A goroutine then waits up to KillGrace and
// escalates to SIGKILL if the target is still alive.
//
// Two self-preservation guards run before any signal is sent:
//
//  1. fast-path: t.PID == os.Getpid() → ErrSelfPID
//  2. scanner re-check: t.PID must appear among live same-uid listeners
//     (Iron Law 1 + Iron Law 2 in a single pass; covers the race where
//     a new scan briefly surfaces our own PID).
func (m *Manager) Kill(ctx context.Context, t Target) error {
	if t.PID == m.selfPID {
		return ErrSelfPID
	}
	if t.PID <= 1 {
		// PID 0 means "the current process group" in kill(2); PID 1 is
		// init. Neither is a legitimate dashboard target and must never
		// reach signal dispatch.
		return fmt.Errorf("%w: refusing PID %d", ErrUnknownPID, t.PID)
	}
	if err := m.verifyLive(ctx, t.PID); err != nil {
		return err
	}
	proc, err := os.FindProcess(t.PID)
	if err != nil {
		return fmt.Errorf("process: find pid %d: %w", t.PID, err)
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		return fmt.Errorf("process: SIGTERM pid %d: %w", t.PID, err)
	}
	go m.escalate(t.PID)
	return nil
}

// verifyLive runs the configured scanner and asserts t.PID shows up in
// its output. The scanner already filters own-pid and cross-uid, so a
// hit proves both guards in one call.
func (m *Manager) verifyLive(ctx context.Context, pid int) error {
	sc, err := m.newScanner()
	if err != nil {
		return fmt.Errorf("process: scanner init: %w", err)
	}
	listeners, err := sc.Scan(ctx)
	if err != nil {
		return fmt.Errorf("process: scan: %w", err)
	}
	for _, l := range listeners {
		if l.PID == pid {
			// Defence-in-depth: the scanner is contracted to exclude
			// os.Getpid(), but re-assert in case a future impl regresses.
			if pid == m.selfPID {
				return ErrSelfPID
			}
			return nil
		}
	}
	return fmt.Errorf("%w: pid=%d", ErrUnknownPID, pid)
}

// escalate waits for KillGrace and issues SIGKILL if the target still
// responds to signal 0. Runs in its own goroutine so Kill stays within
// the 50ms return budget.
func (m *Manager) escalate(pid int) {
	time.Sleep(m.grace)
	if !isAlive(pid) {
		return
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return
	}
	_ = proc.Signal(syscall.SIGKILL)
}

// isAlive probes via signal 0: returns true iff the process exists AND
// we have permission to signal it (sufficient for our own children and
// same-uid listeners; false for reaped, nonexistent, or EPERM).
func isAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

// Restart spawns the remembered entry identified by id in a brand-new
// process group using the stored argv/cwd/env verbatim — caller-supplied
// cwd or argv is not accepted (anti-scenario A4).
func (m *Manager) Restart(ctx context.Context, id string) error {
	if m.store == nil {
		return ErrStoreRequired
	}
	r, err := m.store.FindRemembered(id)
	if err != nil {
		return fmt.Errorf("process: lookup remembered %q: %w", id, err)
	}
	if r == nil {
		return fmt.Errorf("%w: id=%s", ErrRememberedNotFound, id)
	}
	return m.SpawnDetached(ctx, Spec{
		Command: r.Command,
		Cwd:     r.Cwd,
		Env:     r.Env,
	})
}

// SpawnDetached starts argv under a fresh session+pgid with stdio
// redirected to /dev/null and a reaper goroutine to prevent zombies.
// Iron Law 5 — argv is passed as a slice to exec.Command; no shell.
func (m *Manager) SpawnDetached(ctx context.Context, s Spec) error {
	_, err := m.spawn(ctx, s)
	return err
}

// SpawnForTest mirrors SpawnDetached but returns the child's PID so
// acceptance tests can inspect it (e.g. TestA16 verifies pgid).
func (m *Manager) SpawnForTest(ctx context.Context, s Spec) (int, error) {
	return m.spawn(ctx, s)
}

func (m *Manager) spawn(ctx context.Context, s Spec) (int, error) {
	// The whole point of SpawnDetached is that the child outlives the
	// caller's request context. Using exec.Command (not CommandContext)
	// is deliberate — we do not want the http handler's ctx timeout to
	// kill the restarted listener.
	_ = ctx
	if len(s.Command) == 0 {
		return 0, ErrEmptyCommand
	}

	cmd := exec.Command(s.Command[0], s.Command[1:]...)
	cmd.Dir = s.Cwd
	if s.Env != nil {
		cmd.Env = append([]string(nil), s.Env...)
	}

	devNullR, err := os.OpenFile(os.DevNull, os.O_RDONLY, 0)
	if err != nil {
		return 0, fmt.Errorf("process: open %s: %w", os.DevNull, err)
	}
	devNullW, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		_ = devNullR.Close()
		return 0, fmt.Errorf("process: open %s for write: %w", os.DevNull, err)
	}
	cmd.Stdin = devNullR
	cmd.Stdout = devNullW
	cmd.Stderr = devNullW
	// Setsid creates a fresh session AND a new process group whose pgid
	// equals the child's pid — satisfying Iron Law 9 (detach from the
	// dashboard's pgid so SIGHUP/SIGINT propagated by the controlling
	// terminal cannot cascade into the restarted listener). Setpgid is
	// deliberately NOT combined with Setsid here: POSIX forbids changing
	// the pgid of a session leader, and darwin strictly enforces it
	// (setpgid(0,0) after setsid returns EPERM). The original decomposition
	// wording "Setpgid:true, Setsid:true" is redundant — Setsid alone
	// gives the stronger guarantee the Iron Law needs.
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid: true,
	}

	if err := cmd.Start(); err != nil {
		_ = devNullR.Close()
		_ = devNullW.Close()
		return 0, fmt.Errorf("process: start %v: %w", s.Command, err)
	}

	pid := cmd.Process.Pid
	go reap(cmd, devNullR, devNullW)
	return pid, nil
}

// reap awaits the child (preventing zombies) and closes the /dev/null
// fds we opened for its stdio. cmd.Wait's error is intentionally
// discarded: a non-zero exit from the user's own process is normal.
func reap(cmd *exec.Cmd, devNullR, devNullW *os.File) {
	_ = cmd.Wait()
	if devNullR != nil {
		_ = devNullR.Close()
	}
	if devNullW != nil {
		_ = devNullW.Close()
	}
}

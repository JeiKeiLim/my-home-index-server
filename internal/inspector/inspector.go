// Package inspector retrieves per-PID process metadata (argv, cwd,
// start time, uid, env) needed to render the dashboard and to restart
// remembered processes with the same environment they originally ran
// in.
//
// The primary backend is gopsutil v4 (github.com/shirou/gopsutil/v4/process),
// which uses proc_pidpath + PROC_PIDVNODEPATHINFO + PROC_PIDTBSDINFO +
// sysctl KERN_PROCARGS2 under the hood — the syscall set Apple DTS
// recommends for same-uid process introspection. Because
// gopsutil.Environ() returns "not implemented yet" on Darwin, env is
// retrieved via a small cgo helper (envp_darwin.go) wrapping
// sysctl KERN_PROCARGS2.
//
// Darwin env limitation: macOS 26 strips envp[] from KERN_PROCARGS2
// output for cross-process reads (even same-uid). Callers inspecting
// children will see an empty Env slice; only self-reads return the
// full env block. Restart flows that need the original env must
// snapshot it from Store at remember-time, not from a live inspect.
//
// macOS 26 also collapses several distinct sysctl rejection reasons
// into EINVAL on the KERN_PROCARGS2 path: a vanished pid and a
// foreign-uid pid both surface as EINVAL rather than the textbook
// ESRCH/EPERM. readEnv disambiguates with a kill(pid, 0) probe so
// callers still see ErrNotFound vs ErrPermission rather than a raw
// errno.
//
// See .tenet/knowledge/2026-04-17_research-macos-process-metadata.md
// for the field-by-field mapping and rationale.
package inspector

import (
	"context"
	"errors"
	"fmt"
	"syscall"
	"time"

	gops "github.com/shirou/gopsutil/v4/process"
)

// ProcInfo is the flattened per-PID snapshot returned by Inspect.
type ProcInfo struct {
	PID       int
	Command   []string  // argv
	Cwd       string    // absolute, may be empty if not available
	StartTime time.Time // wall clock
	UID       int
	Env       []string // KEY=VAL entries, best-effort
}

// Inspector is the abstraction the server calls when it needs per-PID
// metadata. A single implementation (gopsutil + cgo envp helper) is
// shipped today but the interface keeps the seam testable.
type Inspector interface {
	Inspect(ctx context.Context, pid int) (*ProcInfo, error)
}

// ErrNotFound is returned when the PID no longer exists at the moment
// Inspect is called. Callers treat it like a benign race — the process
// exited between the scan and the inspect.
var ErrNotFound = errors.New("inspector: pid not found")

// ErrPermission is returned when the kernel refuses a metadata read
// because the target process is owned by a different uid. Port-manager
// only inspects same-uid processes in production, but tests running
// under sudo or inspecting root-owned daemons may hit this.
var ErrPermission = errors.New("inspector: permission denied")

// NewGopsutil returns the production Inspector backed by gopsutil v4.
func NewGopsutil() Inspector {
	return &gopsutilInspector{}
}

type gopsutilInspector struct{}

// Inspect collects argv, cwd, start time, uid, and env for pid.
//
// The call intentionally keeps going if individual fields fail: a
// process can die mid-inspection, or a read can hit EPERM for a
// short-lived cross-uid pid. Cwd/Env are best-effort; a missing Cwd or
// Env is represented as the zero value, not an error. Only failures
// that make the whole snapshot useless (process gone, permission
// refused at the kproc level) escalate to a returned error.
func (g *gopsutilInspector) Inspect(ctx context.Context, pid int) (*ProcInfo, error) {
	if pid <= 0 {
		return nil, fmt.Errorf("inspector: invalid pid %d", pid)
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("inspector: %w", err)
	}

	proc, err := gops.NewProcessWithContext(ctx, int32(pid))
	if err != nil {
		return nil, classify(err, pid)
	}

	argv, err := resolveArgv(ctx, proc)
	if err != nil {
		return nil, classify(err, pid)
	}

	// Cwd is best-effort — not every process exposes it. Empty on
	// failure.
	cwd, cwdErr := proc.CwdWithContext(ctx)
	if cwdErr != nil {
		if perr := classify(cwdErr, pid); errors.Is(perr, ErrNotFound) {
			return nil, perr
		}
		cwd = ""
	}

	createTime, err := proc.CreateTimeWithContext(ctx)
	if err != nil {
		return nil, classify(err, pid)
	}

	uids, err := proc.UidsWithContext(ctx)
	if err != nil {
		return nil, classify(err, pid)
	}
	if len(uids) == 0 {
		return nil, fmt.Errorf("inspector: pid %d has no uids", pid)
	}

	// Env on darwin: gopsutil returns ErrNotImplementedError, so we
	// shell out to the sysctl KERN_PROCARGS2 cgo helper. On any other
	// OS (or in tests built with fallback), readEnv returns nil, nil.
	env, envErr := readEnv(ctx, pid)
	if envErr != nil {
		// Cross-uid EPERM is expected when inspecting foreign
		// processes; leave Env empty in that case.
		if !errors.Is(envErr, ErrPermission) {
			return nil, fmt.Errorf("inspector: read env for pid %d: %w", pid, envErr)
		}
		env = nil
	}

	return &ProcInfo{
		PID:       pid,
		Command:   argv,
		Cwd:       cwd,
		StartTime: time.UnixMilli(createTime),
		UID:       int(uids[0]),
		Env:       env,
	}, nil
}

// classify maps gopsutil/OS errors to our two sentinel errors so
// callers can distinguish "process gone" from "permission denied"
// without string-matching.
func classify(err error, pid int) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, gops.ErrorProcessNotRunning) {
		return fmt.Errorf("%w: pid %d", ErrNotFound, pid)
	}
	if errors.Is(err, gops.ErrorNotPermitted) {
		return fmt.Errorf("%w: pid %d", ErrPermission, pid)
	}
	// gopsutil wraps syscall errors from proc_pidinfo / sysctl. Match on
	// the typed syscall.Errno (uintptr) so the assertion actually
	// succeeds — an anonymous interface{ Errno() uintptr } would not,
	// because syscall.Errno does not carry that method.
	var e syscall.Errno
	if errors.As(err, &e) {
		switch e {
		case syscall.EPERM, syscall.EACCES:
			return fmt.Errorf("%w: pid %d (%v)", ErrPermission, pid, e)
		case syscall.ESRCH:
			return fmt.Errorf("%w: pid %d (%v)", ErrNotFound, pid, e)
		}
	}
	// String fallbacks: gopsutil sometimes returns fmt.Errorf("exit
	// status 1") or similar from the kproc path.
	msg := err.Error()
	if containsAny(msg, "no such process", "process does not exist") {
		return fmt.Errorf("%w: pid %d", ErrNotFound, pid)
	}
	if containsAny(msg, "operation not permitted", "permission denied") {
		return fmt.Errorf("%w: pid %d", ErrPermission, pid)
	}
	return fmt.Errorf("inspector: pid %d: %w", pid, err)
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if indexOf(s, sub) >= 0 {
			return true
		}
	}
	return false
}

// indexOf is a tiny replacement for strings.Contains to keep the
// import list smaller; using strings.Contains here would be fine too.
func indexOf(s, sub string) int {
	if sub == "" {
		return 0
	}
	if len(sub) > len(s) {
		return -1
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// argvSource is the subset of *gops.Process used to resolve argv. It
// exists so the CmdlineSlice → Cmdline fallback can be exercised with
// a fake in unit tests; *gops.Process satisfies it implicitly.
type argvSource interface {
	CmdlineSliceWithContext(ctx context.Context) ([]string, error)
	CmdlineWithContext(ctx context.Context) (string, error)
}

// resolveArgv applies the CmdlineSlice → Cmdline fallback chain.
//
// gopsutil's CmdlineSlice is preferred because it preserves argv
// structure (no quoting ambiguity), but on some processes it returns
// an error or an empty slice while the space-joined Cmdline still
// works. In that case we splitCmdline the string — lossy for argv
// entries with embedded spaces, but better than dropping the field.
//
// Returned err is the original CmdlineSlice error and is non-nil only
// when both calls failed; callers classify it.
func resolveArgv(ctx context.Context, p argvSource) ([]string, error) {
	argv, err := p.CmdlineSliceWithContext(ctx)
	if err == nil && len(argv) > 0 {
		return argv, nil
	}
	cmdline, cerr := p.CmdlineWithContext(ctx)
	if cerr == nil && cmdline != "" {
		return splitCmdline(cmdline), nil
	}
	if err != nil {
		return nil, err
	}
	return argv, nil
}

// splitCmdline is a whitespace splitter used only when CmdlineSlice
// fails but Cmdline succeeds. Output is lossy for argv entries that
// contain spaces, but it's still useful for labels and logs.
func splitCmdline(s string) []string {
	var out []string
	start := -1
	for i := 0; i < len(s); i++ {
		if s[i] == ' ' || s[i] == '\t' {
			if start >= 0 {
				out = append(out, s[start:i])
				start = -1
			}
			continue
		}
		if start < 0 {
			start = i
		}
	}
	if start >= 0 {
		out = append(out, s[start:])
	}
	return out
}

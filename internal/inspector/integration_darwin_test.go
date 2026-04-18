//go:build darwin

package inspector

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestInspectChildRoundTrip spawns /bin/sleep with a known cwd and
// verifies the inspector recovers the exact argv, cwd, and uid.
//
// macOS 26 strips envp[] from KERN_PROCARGS2 output for cross-process
// reads as a hardening measure — only self-reads see env. We therefore
// do NOT assert env here; that round-trip is covered in
// TestInspectSelfEnvRoundTrip below.
//
// This is the core behavioural assertion for job-4: given a live
// child we control, Inspect must return its metadata.
func TestInspectChildRoundTrip(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	resolvedCwd, err := filepath.EvalSymlinks(tmp)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}

	cmd := exec.Command("/bin/sleep", "180")
	cmd.Dir = resolvedCwd
	cmd.Env = append(os.Environ(), "PORT_MANAGER_INSPECTOR_TEST=a-very-specific-value-42")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	deadline := time.Now().Add(2 * time.Second)
	var info *ProcInfo
	for {
		info, err = NewGopsutil().Inspect(context.Background(), cmd.Process.Pid)
		if err == nil && len(info.Command) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Inspect child after 2s: err=%v info=%+v", err, info)
		}
		time.Sleep(20 * time.Millisecond)
	}

	if info.PID != cmd.Process.Pid {
		t.Errorf("PID = %d, want %d", info.PID, cmd.Process.Pid)
	}
	if got, want := info.Command, []string{"/bin/sleep", "180"}; !eqStrings(got, want) {
		t.Errorf("Command = %v, want %v", got, want)
	}
	if info.Cwd != resolvedCwd {
		t.Errorf("Cwd = %q, want %q", info.Cwd, resolvedCwd)
	}
	if info.UID != os.Getuid() {
		t.Errorf("UID = %d, want %d", info.UID, os.Getuid())
	}
	if info.StartTime.IsZero() || time.Since(info.StartTime) > time.Minute {
		t.Errorf("StartTime = %v, expected recent", info.StartTime)
	}
	// StartTime must be after the test started (child was spawned
	// inside this test function).
	if info.StartTime.Before(time.Now().Add(-10 * time.Second)) {
		t.Errorf("StartTime %v implausibly old for a just-spawned child", info.StartTime)
	}
}

// TestInspectSelfEnvRoundTrip verifies the KERN_PROCARGS2 env parser
// end-to-end by inspecting the test process itself. Self-reads return
// the full env block (macOS hardening only strips env for
// cross-process reads), so this is where env round-trip is actually
// provable.
func TestInspectSelfEnvRoundTrip(t *testing.T) {
	t.Parallel()

	info, err := NewGopsutil().Inspect(context.Background(), os.Getpid())
	if err != nil {
		t.Fatalf("Inspect(self): %v", err)
	}
	if len(info.Env) == 0 {
		t.Fatal("self-inspect returned empty env; KERN_PROCARGS2 parser regressed")
	}
	// The test binary is exec'd by `go test` with the full shell
	// env, so PATH is guaranteed to be set.
	var gotPath bool
	for _, e := range info.Env {
		if len(e) >= 5 && e[:5] == "PATH=" {
			gotPath = true
			break
		}
	}
	if !gotPath {
		t.Errorf("Env %d entries — expected a PATH= entry", len(info.Env))
	}
}

// TestReadEnvParsesKnownLayout drives parseProcargs directly with a
// hand-crafted KERN_PROCARGS2 buffer to prove the parser handles the
// argc/exec_path/argv/envp sequence correctly — including NUL padding
// after exec_path which classic bugs miss.
func TestReadEnvParsesKnownLayout(t *testing.T) {
	t.Parallel()

	buf := buildProcargsBuffer(3,
		"/usr/bin/example",
		[]string{"example", "-v", "file.txt"},
		[]string{"PATH=/bin", "HOME=/tmp"},
	)
	env, err := parseProcargs(buf)
	if err != nil {
		t.Fatalf("parseProcargs: %v", err)
	}
	want := []string{"PATH=/bin", "HOME=/tmp"}
	if !eqStrings(env, want) {
		t.Errorf("env = %v, want %v", env, want)
	}
}

func TestParseProcargsHandlesShortBuffer(t *testing.T) {
	t.Parallel()

	if _, err := parseProcargs([]byte{0, 0}); err == nil {
		t.Error("expected error for 2-byte buffer")
	}
}

func TestParseProcargsRejectsBogusArgc(t *testing.T) {
	t.Parallel()

	buf := make([]byte, 8)
	binary.LittleEndian.PutUint32(buf[:4], 99999)
	if _, err := parseProcargs(buf); err == nil {
		t.Error("expected error for argc 99999")
	}
}

func TestParseProcargsNegativeArgcRejected(t *testing.T) {
	t.Parallel()

	buf := make([]byte, 8)
	binary.LittleEndian.PutUint32(buf[:4], 0xFFFFFFFF)
	if _, err := parseProcargs(buf); err == nil {
		t.Error("expected error for negative argc")
	}
}

func TestParseProcargsEmptyEnv(t *testing.T) {
	t.Parallel()

	buf := buildProcargsBuffer(1, "/bin/x", []string{"x"}, nil)
	env, err := parseProcargs(buf)
	if err != nil {
		t.Fatalf("parseProcargs: %v", err)
	}
	if len(env) != 0 {
		t.Errorf("env = %v, want empty", env)
	}
}

func TestParseProcargsCrossProcessStrippedEnv(t *testing.T) {
	t.Parallel()

	// Simulates the buffer macOS 26 returns for a cross-process
	// read: argv is present but env is truncated. Parser must
	// return (nil, nil), not an error.
	buf := buildProcargsBuffer(2, "/bin/sleep",
		[]string{"/bin/sleep", "180"}, nil)
	env, err := parseProcargs(buf)
	if err != nil {
		t.Fatalf("parseProcargs: %v", err)
	}
	if env != nil {
		t.Errorf("env = %v, want nil", env)
	}
}

// buildProcargsBuffer constructs a buffer shaped like KERN_PROCARGS2
// output for test input. The kernel pads exec_path to a word
// boundary with NULs before the argv block starts — we replicate
// that by tacking on a few extra NULs.
func buildProcargsBuffer(argc int32, execPath string, argv, env []string) []byte {
	var out []byte
	out = append(out, 0, 0, 0, 0)
	binary.LittleEndian.PutUint32(out[:4], uint32(argc))
	out = append(out, []byte(execPath)...)
	out = append(out, 0, 0, 0, 0) // exec_path NUL + padding
	for _, a := range argv {
		out = append(out, []byte(a)...)
		out = append(out, 0)
	}
	for _, e := range env {
		out = append(out, []byte(e)...)
		out = append(out, 0)
	}
	return out
}

func TestReadEnvCancelledContextReturnsError(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := readEnv(ctx, os.Getpid()); err == nil {
		t.Error("expected cancelled ctx error")
	}
}

func TestReadEnvNonexistentPID(t *testing.T) {
	t.Parallel()

	// 0x7ffffffe is an impossible pid: macOS sysctl returns EINVAL
	// rather than ESRCH on macOS 26, but readEnv's kill(pid, 0) probe
	// disambiguates and we MUST surface ErrNotFound — not a raw errno
	// — so callers can treat the race as benign.
	_, err := readEnv(context.Background(), 0x7ffffffe)
	if err == nil {
		t.Fatal("expected error for impossible pid")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want errors.Is(ErrNotFound)", err)
	}
}

// TestReadEnvCrossUIDReturnsPermission inspects launchd (pid 1, uid 0)
// and asserts we classify the kernel rejection rather than leaking the
// raw errno. macOS 26 may surface this as EPERM (clean cross-uid
// rejection) OR EINVAL (env-stripped + sysctl refusal) depending on
// the exact sub-release; either is acceptable as long as the error
// resolves to one of our two sentinels and not a bare syscall.Errno.
func TestReadEnvCrossUIDReturnsPermission(t *testing.T) {
	t.Parallel()

	if os.Getuid() == 0 {
		t.Skip("cannot test cross-uid when running as root")
	}

	_, err := readEnv(context.Background(), 1)
	if err == nil {
		t.Fatal("expected error reading env of pid 1 from non-root uid")
	}
	if !errors.Is(err, ErrPermission) && !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want errors.Is(ErrPermission) or errors.Is(ErrNotFound) — raw errno leaked", err)
	}
}

func TestReadEnvSelfReturnsNonEmpty(t *testing.T) {
	t.Parallel()

	env, err := readEnv(context.Background(), os.Getpid())
	if err != nil {
		t.Fatalf("readEnv(self): %v", err)
	}
	if len(env) == 0 {
		t.Fatal("self env empty — parser regressed")
	}
}

func eqStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

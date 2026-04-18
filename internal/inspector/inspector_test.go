package inspector

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"syscall"
	"testing"
	"time"

	gops "github.com/shirou/gopsutil/v4/process"
)

func TestInspectSelfReportsKnownFields(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	info, err := NewGopsutil().Inspect(ctx, os.Getpid())
	if err != nil {
		t.Fatalf("Inspect(self): %v", err)
	}
	if info.PID != os.Getpid() {
		t.Errorf("PID = %d, want %d", info.PID, os.Getpid())
	}
	if len(info.Command) == 0 {
		t.Errorf("Command empty — expected at least argv[0]")
	}
	// UID should match the uid we run under.
	if info.UID != os.Getuid() {
		t.Errorf("UID = %d, want %d", info.UID, os.Getuid())
	}
	// StartTime should be in the past and within the last day.
	if info.StartTime.IsZero() {
		t.Error("StartTime zero")
	}
	if time.Since(info.StartTime) < 0 || time.Since(info.StartTime) > 24*time.Hour {
		t.Errorf("StartTime %v not in last 24h", info.StartTime)
	}
	// Cwd: either populated with an absolute path, or empty if the
	// kernel refused — but since this is the same-uid case it should
	// normally succeed.
	if info.Cwd == "" {
		t.Log("Cwd empty — acceptable but unexpected for same-uid self inspect")
	} else if info.Cwd[0] != '/' {
		t.Errorf("Cwd %q is not absolute", info.Cwd)
	}
}

func TestInspectRejectsInvalidPID(t *testing.T) {
	t.Parallel()

	_, err := NewGopsutil().Inspect(context.Background(), 0)
	if err == nil {
		t.Fatal("expected error for pid 0")
	}
	_, err = NewGopsutil().Inspect(context.Background(), -1)
	if err == nil {
		t.Fatal("expected error for pid -1")
	}
}

func TestInspectNonExistentPIDIsNotFound(t *testing.T) {
	t.Parallel()

	// PID 0x7fff_fffe is effectively guaranteed not to exist on
	// darwin — the kernel tops out well below 2^31.
	_, err := NewGopsutil().Inspect(context.Background(), 0x7ffffffe)
	if err == nil {
		t.Fatal("expected ErrNotFound for impossible pid")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestInspectCancelledContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewGopsutil().Inspect(ctx, os.Getpid())
	if err == nil {
		t.Fatal("expected context error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestClassifyMapsSyscallErrors(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		input   error
		wantErr error
	}{
		{"nil returns nil", nil, nil},
		{"not-running", wrapErr("process does not exist"), ErrNotFound},
		{"operation not permitted", wrapErr("operation not permitted"), ErrPermission},
		{"permission denied", wrapErr("permission denied"), ErrPermission},

		// Sentinel errors from gopsutil — these are the contractual
		// hooks we promise callers (ErrNotFound for "process gone",
		// ErrPermission for cross-uid). If gopsutil ever stops
		// returning these the test will fail loudly rather than
		// silently degrading to the string-fallback path.
		{"gops ErrorProcessNotRunning", gops.ErrorProcessNotRunning, ErrNotFound},
		{"wrapped gops ErrorProcessNotRunning", fmt.Errorf("inspect: %w", gops.ErrorProcessNotRunning), ErrNotFound},
		{"gops ErrorNotPermitted", gops.ErrorNotPermitted, ErrPermission},

		// Typed syscall.Errno path — proves the typed errors.As
		// assertion replaces the dead anonymous-interface code.
		{"syscall EPERM", syscall.EPERM, ErrPermission},
		{"syscall EACCES", syscall.EACCES, ErrPermission},
		{"syscall ESRCH", syscall.ESRCH, ErrNotFound},
		{"wrapped syscall EPERM", fmt.Errorf("kproc: %w", syscall.EPERM), ErrPermission},
		{"wrapped syscall ESRCH", fmt.Errorf("kproc: %w", syscall.ESRCH), ErrNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classify(tc.input, 42)
			if tc.wantErr == nil {
				if got != nil {
					t.Fatalf("classify(nil) = %v, want nil", got)
				}
				return
			}
			if !errors.Is(got, tc.wantErr) {
				t.Errorf("classify(%v) = %v, want errors.Is(%v)", tc.input, got, tc.wantErr)
			}
		})
	}
}

// TestClassifySyscallEINVALIsNotMisclassified guards against the prior
// regression where an anonymous interface{Errno()uintptr} assertion
// silently failed and EINVAL got passed through unchanged. With the
// typed syscall.Errno switch, EINVAL deliberately doesn't map to
// either sentinel — it bubbles up so callers can see the real cause
// rather than mis-pretending the pid is gone.
func TestClassifySyscallEINVALIsNotMisclassified(t *testing.T) {
	t.Parallel()

	got := classify(syscall.EINVAL, 42)
	if errors.Is(got, ErrNotFound) || errors.Is(got, ErrPermission) {
		t.Errorf("classify(EINVAL) = %v, must NOT match ErrNotFound or ErrPermission", got)
	}
	if !errors.Is(got, syscall.EINVAL) {
		t.Errorf("classify(EINVAL) = %v, lost the underlying errno", got)
	}
}

// fakeArgvSource satisfies the argvSource interface so we can drive
// resolveArgv's CmdlineSlice → Cmdline fallback path with controlled
// errors and outputs without touching real processes.
type fakeArgvSource struct {
	sliceArgv []string
	sliceErr  error
	cmdline   string
	cmdErr    error
}

func (f *fakeArgvSource) CmdlineSliceWithContext(_ context.Context) ([]string, error) {
	return f.sliceArgv, f.sliceErr
}
func (f *fakeArgvSource) CmdlineWithContext(_ context.Context) (string, error) {
	return f.cmdline, f.cmdErr
}

func TestInspect_CmdlineSliceErrFallsBackToCmdline(t *testing.T) {
	t.Parallel()

	src := &fakeArgvSource{
		sliceErr: errors.New("CmdlineSlice broken"),
		cmdline:  "a b c",
	}
	argv, err := resolveArgv(context.Background(), src)
	if err != nil {
		t.Fatalf("resolveArgv: %v", err)
	}
	want := []string{"a", "b", "c"}
	if !reflect.DeepEqual(argv, want) {
		t.Errorf("argv = %v, want %v", argv, want)
	}
}

func TestInspect_CmdlineSliceEmptyFallsBackToCmdline(t *testing.T) {
	t.Parallel()

	src := &fakeArgvSource{
		sliceArgv: nil, // empty argv from gopsutil
		cmdline:   "/bin/foo --flag value",
	}
	argv, err := resolveArgv(context.Background(), src)
	if err != nil {
		t.Fatalf("resolveArgv: %v", err)
	}
	want := []string{"/bin/foo", "--flag", "value"}
	if !reflect.DeepEqual(argv, want) {
		t.Errorf("argv = %v, want %v", argv, want)
	}
}

func TestInspect_CmdlineSliceSucceedsSkipsFallback(t *testing.T) {
	t.Parallel()

	// Cmdline is intentionally garbage: if the fallback runs, we'll
	// see "X Y" in the result instead of the slice argv.
	src := &fakeArgvSource{
		sliceArgv: []string{"keep", "me"},
		cmdline:   "X Y",
	}
	argv, err := resolveArgv(context.Background(), src)
	if err != nil {
		t.Fatalf("resolveArgv: %v", err)
	}
	want := []string{"keep", "me"}
	if !reflect.DeepEqual(argv, want) {
		t.Errorf("argv = %v, want %v (fallback ran when it shouldn't have)", argv, want)
	}
}

func TestInspect_CmdlineSliceErrAndCmdlineErrSurfacesSliceErr(t *testing.T) {
	t.Parallel()

	sliceErr := errors.New("primary failure")
	src := &fakeArgvSource{
		sliceErr: sliceErr,
		cmdErr:   errors.New("fallback failure"),
	}
	_, err := resolveArgv(context.Background(), src)
	if !errors.Is(err, sliceErr) {
		t.Errorf("err = %v, want sliceErr to surface so classify() sees it", err)
	}
}

func wrapErr(s string) error { return errors.New(s) }

func TestSplitCmdline(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"   ", nil},
		{"foo", []string{"foo"}},
		{"foo bar", []string{"foo", "bar"}},
		{"  foo  bar\tbaz  ", []string{"foo", "bar", "baz"}},
	}
	for _, tc := range cases {
		got := splitCmdline(tc.in)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("splitCmdline(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestContainsAny(t *testing.T) {
	t.Parallel()

	if !containsAny("operation not permitted", "not permitted", "missing") {
		t.Error("expected match on substring")
	}
	if containsAny("hello world", "xyz", "qqq") {
		t.Error("unexpected match")
	}
	if containsAny("anything", "") != true {
		// indexOf("", "") returns 0; the empty substring always matches.
		t.Error("empty needle should match")
	}
}

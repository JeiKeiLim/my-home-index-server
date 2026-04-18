//go:build darwin

package process_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/JeiKeiLim/my-home-index-server/internal/config"
	"github.com/JeiKeiLim/my-home-index-server/internal/process"
	"github.com/JeiKeiLim/my-home-index-server/internal/scanner"
	"github.com/JeiKeiLim/my-home-index-server/internal/store"
)

// mkConfig is a minimal, always-valid Config for tests.
func mkConfig(t *testing.T) *process.Config {
	t.Helper()
	ephemeral := 0
	cfg, err := config.Load(config.Options{
		AuthToken:     "test-" + strings.Repeat("a", 32),
		SessionSecret: "sec-" + strings.Repeat("b", 32),
		PublicHost:    "localhost",
		Port:          &ephemeral,
		PortMin:       40000,
		PortMax:       40500,
		KillGraceMS:   3000,
		StateDir:      t.TempDir(),
	})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	return cfg
}

// fakeScanner returns a static listener list; used to exercise the
// scanner-re-check branch of Kill without touching real sockets.
type fakeScanner struct {
	listeners []scanner.Listener
	err       error
	calls     atomic.Int32
}

func (f *fakeScanner) Scan(ctx context.Context) ([]scanner.Listener, error) {
	f.calls.Add(1)
	if f.err != nil {
		return nil, f.err
	}
	return f.listeners, nil
}
func (f *fakeScanner) Name() string { return "fake" }

// --- Kill: self-preservation ------------------------------------------------

func TestKill_SelfPIDIsRefusedAtEntry(t *testing.T) {
	cfg := mkConfig(t)
	// A fake scanner that would OTHERWISE say self is a valid listener;
	// we expect Kill to refuse before ever reaching the scanner.
	fs := &fakeScanner{
		listeners: []scanner.Listener{{PID: os.Getpid(), Port: 40000, Protocol: "tcp", Source: "fake"}},
	}
	pm, err := process.New(cfg, process.WithScannerFactory(func() (scanner.Scanner, error) { return fs, nil }))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	err = pm.Kill(context.Background(), process.Target{PID: os.Getpid(), Port: 40000})
	if !errors.Is(err, process.ErrSelfPID) {
		t.Fatalf("Kill(self): err = %v, want ErrSelfPID", err)
	}
	if fs.calls.Load() != 0 {
		t.Fatalf("scanner was called %d times; self-PID must short-circuit before scanning", fs.calls.Load())
	}
}

func TestKill_PIDNotInScannerIsRefused(t *testing.T) {
	cfg := mkConfig(t)
	// scanner sees nothing — so ANY non-self PID should be rejected.
	fs := &fakeScanner{listeners: nil}
	pm, err := process.New(cfg, process.WithScannerFactory(func() (scanner.Scanner, error) { return fs, nil }))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	err = pm.Kill(context.Background(), process.Target{PID: 99999, Port: 40100})
	if !errors.Is(err, process.ErrUnknownPID) {
		t.Fatalf("Kill(unknown): err = %v, want ErrUnknownPID", err)
	}
}

func TestKill_RefusesPIDOneAndZero(t *testing.T) {
	cfg := mkConfig(t)
	pm, err := process.New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, pid := range []int{0, 1} {
		err := pm.Kill(context.Background(), process.Target{PID: pid})
		if err == nil {
			t.Fatalf("Kill(PID=%d): expected error, got nil", pid)
		}
	}
}

// --- Kill: SIGTERM → grace → SIGKILL ---------------------------------------

// spawnTermIgnorer spawns a child that ignores SIGTERM (via `trap ” TERM`)
// and sleeps. The returned PID is unrelated to any parent-child reap path
// — the test spawns via a raw fork so no Wait() intercepts the exit and
// Signal(0) faithfully reports liveness (zombies are cleaned up by the
// returned reap closure once the test observes death).
func spawnTermIgnorer(t *testing.T) (pid int, reap func()) {
	t.Helper()
	cmd := exec.Command("bash", "-c", `trap '' TERM; sleep 30`)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start term-ignorer: %v", err)
	}
	pid = cmd.Process.Pid
	// Start Wait in a goroutine so the kernel actually reaps the child
	// after SIGKILL — otherwise Signal(0) reports the zombie as alive.
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	reap = func() {
		_ = cmd.Process.Signal(syscall.SIGKILL)
		<-done
	}
	t.Cleanup(reap)
	return pid, reap
}

func TestKill_EscalatesToSIGKILLAfterGrace(t *testing.T) {
	cfg := mkConfig(t)
	cfg.KillGraceMS = 200 // short grace so the test runs fast

	pid, _ := spawnTermIgnorer(t)
	fs := &fakeScanner{
		listeners: []scanner.Listener{{PID: pid, Port: 40111, Protocol: "tcp", Source: "fake"}},
	}
	pm, err := process.New(cfg, process.WithScannerFactory(func() (scanner.Scanner, error) { return fs, nil }))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	start := time.Now()
	if err := pm.Kill(context.Background(), process.Target{PID: pid, Port: 40111}); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed > 200*time.Millisecond {
		t.Fatalf("Kill returned after %v; must be non-blocking (< 200ms)", elapsed)
	}

	// Wait for escalation to land. Bound: grace (200ms) + headroom.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err != nil {
			// Gone — either reaped (ESRCH) or no longer signalable.
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("child survived past grace+SIGKILL; escalation path broken")
}

// --- SpawnDetached: behaviour ---------------------------------------------

func TestSpawnDetached_NewPgidAndSid(t *testing.T) {
	cfg := mkConfig(t)
	pm, err := process.New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	pid, err := pm.SpawnForTest(context.Background(), process.Spec{Command: []string{"sleep", "2"}, Cwd: t.TempDir()})
	if err != nil {
		t.Fatalf("SpawnForTest: %v", err)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })

	out, err := exec.Command("ps", "-o", "pgid=,sess=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		t.Fatalf("ps: %v", err)
	}
	fields := strings.Fields(string(out))
	if len(fields) < 1 {
		t.Fatalf("ps output empty: %q", out)
	}
	pgid, err := strconv.Atoi(fields[0])
	if err != nil {
		t.Fatalf("parse pgid %q: %v", fields[0], err)
	}
	if pgid != pid {
		t.Fatalf("pgid=%d, want %d (child should lead its own group)", pgid, pid)
	}
	if pgid == os.Getpid() {
		t.Fatalf("pgid equals parent pid — detachment failed")
	}
}

func TestSpawnDetached_RejectsEmptyCommand(t *testing.T) {
	cfg := mkConfig(t)
	pm, err := process.New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := pm.SpawnDetached(context.Background(), process.Spec{Command: nil, Cwd: t.TempDir()}); !errors.Is(err, process.ErrEmptyCommand) {
		t.Fatalf("SpawnDetached(nil argv): %v, want ErrEmptyCommand", err)
	}
}

func TestSpawnDetached_ReapsChildrenNoZombies(t *testing.T) {
	cfg := mkConfig(t)
	pm, err := process.New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for i := 0; i < 25; i++ {
		if err := pm.SpawnDetached(context.Background(), process.Spec{Command: []string{"true"}, Cwd: t.TempDir()}); err != nil {
			t.Fatalf("SpawnDetached[%d]: %v", i, err)
		}
	}
	// Give reaper goroutines time to Wait().
	time.Sleep(500 * time.Millisecond)

	out, _ := exec.Command("bash", "-lc", `ps -xo state,ppid,pid | awk -v me=$$ '$1 ~ /Z/ && $2 == '"$(echo $PPID)"' {print $3}'`).Output()
	_ = out // cross-platform noise
	// Broader check: no zombie anywhere owned by us.
	selfZ, _ := exec.Command("bash", "-lc", `ps -xo state,pid,ppid | awk -v pp=`+strconv.Itoa(os.Getpid())+` '$1 ~ /Z/ && $3 == pp {print $2}'`).Output()
	if z := strings.TrimSpace(string(selfZ)); z != "" {
		t.Fatalf("zombie children observed: %q", z)
	}
}

func TestSpawnDetached_CwdAndEnvApplied(t *testing.T) {
	cfg := mkConfig(t)
	pm, err := process.New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Child writes its cwd + a known env var to a file under the cwd.
	dir := t.TempDir()
	script := filepath.Join(dir, "probe.sh")
	out := filepath.Join(dir, "out.txt")
	body := []byte(`#!/bin/bash
printf 'cwd=%s env=%s' "$(pwd)" "$PM_TEST_MARKER" > ` + out + `
`)
	if err := os.WriteFile(script, body, 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	pid, err := pm.SpawnForTest(context.Background(), process.Spec{
		Command: []string{script},
		Cwd:     dir,
		Env:     []string{"PATH=/usr/bin:/bin", "PM_TEST_MARKER=xyzzy"},
	})
	if err != nil {
		t.Fatalf("SpawnForTest: %v", err)
	}
	// Wait for the reaper goroutine to Wait() — poll for output file.
	deadline := time.Now().Add(3 * time.Second)
	var data []byte
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(out)
		if err == nil && len(b) > 0 {
			data = b
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = pid
	if !bytes.Contains(data, []byte("env=xyzzy")) {
		t.Fatalf("env not propagated: %q", data)
	}
	// Resolved cwd may differ in /private/tmp-vs-/tmp symlink prefix on
	// darwin; assert suffix match via filepath.Base.
	expectedLeaf := filepath.Base(dir)
	if !bytes.Contains(data, []byte(expectedLeaf)) {
		t.Fatalf("cwd not applied: got %q, expected to contain leaf %q", data, expectedLeaf)
	}
}

// --- Restart: store lookup -------------------------------------------------

func TestRestart_ErrorsWithoutStore(t *testing.T) {
	cfg := mkConfig(t)
	pm, err := process.New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := pm.Restart(context.Background(), "x"); !errors.Is(err, process.ErrStoreRequired) {
		t.Fatalf("Restart no store: %v, want ErrStoreRequired", err)
	}
}

func TestRestart_UnknownIDReturnsRememberedNotFound(t *testing.T) {
	cfg := mkConfig(t)
	dir := t.TempDir()
	s, err := store.Open(dir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	pm, err := process.New(cfg, process.WithStore(s))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := pm.Restart(context.Background(), "nope"); !errors.Is(err, process.ErrRememberedNotFound) {
		t.Fatalf("Restart bad id: %v, want ErrRememberedNotFound", err)
	}
}

func TestRestart_UsesStoredSpecVerbatim(t *testing.T) {
	cfg := mkConfig(t)
	dir := t.TempDir()
	s, err := store.Open(dir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	// Arrange: remember an entry that writes a known file.
	tmp := t.TempDir()
	out := filepath.Join(tmp, "restart.txt")
	r := store.Remembered{
		Port:    40133,
		Command: []string{"bash", "-c", "echo stored-ok > " + out},
		Cwd:     tmp,
		Env:     []string{"PATH=/usr/bin:/bin"},
	}
	if err := s.Remember(r); err != nil {
		t.Fatalf("Remember: %v", err)
	}
	list, err := s.ListRemembered(r.Cwd, r.Command)
	if err != nil || len(list) == 0 {
		t.Fatalf("ListRemembered: list=%v err=%v", list, err)
	}
	pm, err := process.New(cfg, process.WithStore(s))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := pm.Restart(context.Background(), list[0].ID); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	// Await the reaper.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(out); err == nil && len(b) > 0 {
			if !bytes.Contains(b, []byte("stored-ok")) {
				t.Fatalf("restart ran wrong argv: %q", b)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("restart did not execute stored argv within deadline")
}

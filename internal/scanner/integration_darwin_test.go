//go:build darwin

package scanner_test

import (
	"bufio"
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/JeiKeiLim/my-home-index-server/internal/scanner"
)

// freeEphemeralPort picks a kernel-assigned port, releases it, and
// returns the number. The short release window is adequate for tests
// because SO_REUSEADDR is set on the spawned child.
func freeEphemeralPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ephemeral listen: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}

// freeEphemeralUDPPort mirrors freeEphemeralPort for UDP, so the UDP
// anti-scenario picks a port unlikely to collide with anything else
// the tester is running.
func freeEphemeralUDPPort(t *testing.T) int {
	t.Helper()
	c, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ephemeral udp listen: %v", err)
	}
	port := c.LocalAddr().(*net.UDPAddr).Port
	_ = c.Close()
	return port
}

// spawnPythonListener starts a Python3 TCP listener on the given port
// and blocks until it prints READY. The returned pid is the listener's
// OS pid; the returned cleanup kills and reaps the child.
func spawnPythonListener(t *testing.T, port int) (pid int, cleanup func()) {
	t.Helper()
	code := "import socket,time,sys\n" +
		"s=socket.socket(socket.AF_INET,socket.SOCK_STREAM)\n" +
		"s.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1)\n" +
		"s.bind(('127.0.0.1'," + strconv.Itoa(port) + "))\n" +
		"s.listen(4)\n" +
		"sys.stdout.write('READY\\n');sys.stdout.flush()\n" +
		"time.sleep(30)\n"
	return spawnPythonHelper(t, code)
}

// spawnPythonUDPListener starts a Python3 UDP socket bound to port and
// blocks until READY. Used by TestA9 to verify TCP-only scanners do not
// surface UDP sockets.
func spawnPythonUDPListener(t *testing.T, port int) (pid int, cleanup func()) {
	t.Helper()
	code := "import socket,time,sys\n" +
		"s=socket.socket(socket.AF_INET,socket.SOCK_DGRAM)\n" +
		"s.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1)\n" +
		"s.bind(('127.0.0.1'," + strconv.Itoa(port) + "))\n" +
		"sys.stdout.write('READY\\n');sys.stdout.flush()\n" +
		"time.sleep(30)\n"
	return spawnPythonHelper(t, code)
}

func spawnPythonHelper(t *testing.T, code string) (int, func()) {
	t.Helper()
	cmd := exec.Command("python3", "-c", code)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("python start: %v", err)
	}
	cleanup := func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}

	rdy := make(chan error, 1)
	go func() {
		s := bufio.NewScanner(stdout)
		if !s.Scan() {
			rdy <- s.Err()
			return
		}
		if s.Text() != "READY" {
			rdy <- net.ErrClosed
			return
		}
		rdy <- nil
	}()
	select {
	case err := <-rdy:
		if err != nil {
			cleanup()
			t.Fatalf("python ready: %v", err)
		}
	case <-time.After(3 * time.Second):
		cleanup()
		t.Fatal("python listener never signaled READY")
	}
	// Brief settle so the kernel flips the socket to LISTEN before we scan.
	time.Sleep(50 * time.Millisecond)
	return cmd.Process.Pid, cleanup
}

// scannerImpls is the matrix used by every per-impl test so adding a
// new scanner requires touching one line.
var scannerImpls = []string{"libproc", "lsof"}

func newScanner(t *testing.T, impl string, cfg *scanner.Config) scanner.Scanner {
	t.Helper()
	scCfg := *cfg
	scCfg.Scanner = impl
	sc, err := scanner.Auto(&scCfg)
	if err != nil {
		t.Fatalf("Auto(%s): %v", impl, err)
	}
	if sc.Name() != impl {
		t.Fatalf("Name() = %q, want %q", sc.Name(), impl)
	}
	return sc
}

// TestIntegration_BothScannersSeePythonListener spawns a Python TCP
// listener on an ephemeral port in the scanner's range and asserts
// that both the libproc and lsof implementations report it, with the
// correct pid/port/protocol/source and excluding our own pid.
func TestIntegration_BothScannersSeePythonListener(t *testing.T) {
	port := freeEphemeralPort(t)
	childPID, cleanup := spawnPythonListener(t, port)
	defer cleanup()

	cfg := &scanner.Config{
		PortMin: port,
		PortMax: port,
	}

	for _, impl := range scannerImpls {
		impl := impl
		t.Run(impl, func(t *testing.T) {
			sc := newScanner(t, impl, cfg)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			ls, err := sc.Scan(ctx)
			if err != nil {
				t.Fatalf("Scan: %v", err)
			}
			var found *scanner.Listener
			for i := range ls {
				if ls[i].PID == childPID && ls[i].Port == port {
					found = &ls[i]
					break
				}
			}
			if found == nil {
				t.Fatalf("%s scanner did not report pid=%d port=%d; got %+v", impl, childPID, port, ls)
			}
			if found.Protocol != "tcp" {
				t.Errorf("Protocol = %q, want tcp", found.Protocol)
			}
			if found.Source != impl {
				t.Errorf("Source = %q, want %q", found.Source, impl)
			}
			if len(found.Addrs) == 0 {
				t.Errorf("Addrs empty")
			}
			for _, l := range ls {
				if l.PID == os.Getpid() {
					t.Errorf("own pid leaked: %+v", l)
				}
			}
		})
	}
}

// TestScanner_ExcludesOwnPID opens a listener *inside* the test
// process and asserts neither scanner returns it. Matches the
// verification bullet "Scanner excludes own PID (os.Getpid())".
func TestScanner_ExcludesOwnPID(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer l.Close()
	port := l.Addr().(*net.TCPAddr).Port

	for _, impl := range scannerImpls {
		impl := impl
		t.Run(impl, func(t *testing.T) {
			sc := newScanner(t, impl, &scanner.Config{PortMin: port, PortMax: port})
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			ls, err := sc.Scan(ctx)
			if err != nil {
				t.Fatalf("Scan: %v", err)
			}
			for _, e := range ls {
				if e.PID == os.Getpid() {
					t.Fatalf("%s returned own pid for port %d: %+v", impl, port, e)
				}
			}
		})
	}
}

// TestScanner_ExcludesOutOfRangePorts verifies the port window filter
// against BOTH implementations end-to-end (real subprocess + real
// listener), not just the parser-level synthetic fixture. Covers the
// per-impl coverage matrix row "port-range filter".
func TestScanner_ExcludesOutOfRangePorts(t *testing.T) {
	port := freeEphemeralPort(t)
	_, cleanup := spawnPythonListener(t, port)
	defer cleanup()

	for _, impl := range scannerImpls {
		impl := impl
		t.Run(impl, func(t *testing.T) {
			// Configure a range that explicitly excludes the child's port.
			sc := newScanner(t, impl, &scanner.Config{PortMin: port + 1, PortMax: port + 100})
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			ls, err := sc.Scan(ctx)
			if err != nil {
				t.Fatalf("Scan: %v", err)
			}
			for _, e := range ls {
				if e.Port == port {
					t.Fatalf("%s: port %d slipped through range filter: %+v", impl, port, e)
				}
			}
		})
	}
}

// TestA9_ScannerIgnoresUDP is the integration-level UDP anti-scenario.
// Spawn a SOCK_DGRAM listener on a free UDP port; require that BOTH
// libproc (whose filter is soi_kind==SOCKINFO_TCP && state==LISTEN)
// AND lsof (whose argv is -iTCP and parseLsofF rejects non-TCP P
// records) return zero results in that port window. Without this
// integration test, libproc's UDP filter is not exercised at all —
// the unit test in scanner_test.go only covers the parseLsofF parser.
func TestA9_ScannerIgnoresUDP(t *testing.T) {
	port := freeEphemeralUDPPort(t)
	_, cleanup := spawnPythonUDPListener(t, port)
	defer cleanup()

	for _, impl := range scannerImpls {
		impl := impl
		t.Run(impl, func(t *testing.T) {
			sc := newScanner(t, impl, &scanner.Config{PortMin: port, PortMax: port})
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			ls, err := sc.Scan(ctx)
			if err != nil {
				t.Fatalf("Scan: %v", err)
			}
			for _, e := range ls {
				if e.Port == port {
					t.Fatalf("%s leaked UDP socket on port %d: %+v", impl, port, e)
				}
			}
		})
	}
}

// TestA10_ScannerRespectsContextTimeout enforces Iron Law 6 against
// BOTH implementations. Two cases are exercised per impl:
//   - pre-cancelled ctx → Scan must return ctx.Err() (context.Canceled)
//   - 1µs deadline ctx (slept past) → Scan must return an error
//
// The lsof path was already covered; libproc was not. Without this,
// the libproc scan-budget wrap (context.WithTimeout(ctx, scanBudget))
// is untested.
func TestA10_ScannerRespectsContextTimeout(t *testing.T) {
	cfg := &scanner.Config{PortMin: 40000, PortMax: 40500}

	for _, impl := range scannerImpls {
		impl := impl
		t.Run(impl+"/precancelled", func(t *testing.T) {
			sc := newScanner(t, impl, cfg)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if _, err := sc.Scan(ctx); err == nil {
				t.Fatalf("%s: expected error from pre-cancelled ctx", impl)
			}
		})
		t.Run(impl+"/tightdeadline", func(t *testing.T) {
			sc := newScanner(t, impl, cfg)
			ctx, cancel := context.WithTimeout(context.Background(), 1*time.Microsecond)
			defer cancel()
			// Sleep past the deadline so the very first ctx.Err()
			// check inside Scan is guaranteed to fire — eliminates
			// flake where 1µs hasn't actually elapsed at entry.
			time.Sleep(2 * time.Millisecond)
			if _, err := sc.Scan(ctx); err == nil {
				t.Fatalf("%s: expected error from expired 1µs deadline", impl)
			}
		})
	}
}

// TestA11_NoFDLeakOverManyScans is the subprocess-fd-leak anti-scenario
// from §A11 of the decomposition. The actual risk surface is the lsof
// path (each Scan forks/execs and pipes stdout); the libproc path is
// tested too as a regression guard against future fd opens. After 500
// iterations of each impl, fd-table growth must stay ≤ 20.
//
// Note: 500 lsof spawns is intentionally heavy — the whole point of
// the scenario is to catch leaks at scale. Run with -short to skip
// when iterating locally.
func TestA11_NoFDLeakOverManyScans(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping 500-iteration fd-leak loop under -short")
	}
	cfg := &scanner.Config{PortMin: 40000, PortMax: 40500}
	const iters = 500
	const fdGrowthBudget = 20

	for _, impl := range scannerImpls {
		impl := impl
		t.Run(impl, func(t *testing.T) {
			sc := newScanner(t, impl, cfg)
			start := countOpenFDs(t)
			for i := 0; i < iters; i++ {
				ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
				_, err := sc.Scan(ctx)
				cancel()
				if err != nil {
					t.Fatalf("%s scan %d: %v", impl, i, err)
				}
			}
			end := countOpenFDs(t)
			if end-start > fdGrowthBudget {
				t.Fatalf("%s: fd growth %d over %d scans exceeds %d (start=%d end=%d)",
					impl, end-start, iters, fdGrowthBudget, start, end)
			}
		})
	}
}

// TestListAllPIDs_NoTruncation guards the proc_listallpids return-value
// convention: the libproc wrapper returns the **count of pids written**,
// not the byte count. An earlier implementation divided by sizeof(pid_t)
// thinking the return was bytes, silently truncating the pid table to
// ~1/4 and causing the scanner to miss any listener whose pid fell past
// the cut. Symptom in production: a TCP listener reachable over the
// network simply did not appear in the dashboard.
//
// We assert listAllPIDs returns a count consistent with the real system
// pid count observed via `ps -A`. A broken divide-by-4 would return
// ≤ ~1/3 of ps's count; we require ≥ 1/2 to stay robust against minor
// races without masking the regression.
func TestListAllPIDs_NoTruncation(t *testing.T) {
	out, err := exec.Command("ps", "-A", "-o", "pid=").Output()
	if err != nil {
		t.Fatalf("ps: %v", err)
	}
	psCount := strings.Count(strings.TrimSpace(string(out)), "\n") + 1
	if psCount < 50 {
		t.Fatalf("ps returned %d pids; this test needs a system with ≥50 processes to be meaningful", psCount)
	}

	pids, err := scanner.ListAllPIDsForTest()
	if err != nil {
		t.Fatalf("listAllPIDs: %v", err)
	}
	if len(pids) < psCount/2 {
		t.Fatalf("listAllPIDs returned %d pids; ps reports %d. Scanner is truncating the pid table — regression of the proc_listallpids byte-vs-count bug.",
			len(pids), psCount)
	}
}

// countOpenFDs returns the number of open fds in the current process.
// Uses /dev/fd directly (per-process fd directory on darwin) — no
// subprocess, so it is safe under stuck-filesystem conditions and
// cannot itself perturb the count it measures. This replaces an
// earlier `lsof -p $$` implementation that violated the same Iron Law
// the production scanner enforces (no unbounded subprocess, ctx-aware,
// -b for stuck mounts).
func countOpenFDs(t *testing.T) int {
	t.Helper()
	matches, err := filepath.Glob("/dev/fd/*")
	if err != nil {
		t.Fatalf("glob /dev/fd: %v", err)
	}
	return len(matches)
}

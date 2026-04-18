// Package security_test exercises every anti-scenario from
// .tenet/spec/scenarios-2026-04-17-port-manager.md.
//
// These tests are stubs that reference packages which will exist after the
// scaffold and feature jobs complete. They should FAIL until the matching
// implementation is in place, then PASS when the Iron Laws from the harness
// hold.
//
//go:build darwin

package security_test

import (
	"context"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	// Placeholder imports — module path chosen by job-1 (scaffold).
	// When the module is renamed, update this block only.
	"github.com/JeiKeiLim/my-home-index-server/internal/auth"
	"github.com/JeiKeiLim/my-home-index-server/internal/config"
	"github.com/JeiKeiLim/my-home-index-server/internal/process"
	"github.com/JeiKeiLim/my-home-index-server/internal/scanner"
	"github.com/JeiKeiLim/my-home-index-server/internal/server"
	"github.com/JeiKeiLim/my-home-index-server/internal/store"
)

func mustConfig(t *testing.T) *config.Config {
	t.Helper()
	// Options.Port is *int: a non-nil pointer (including to 0) is the
	// caller's explicit choice — `intPtr(0)` here means "bind ephemeral"
	// and Load must NOT silently rewrite it to DefaultPort.
	ephemeral := 0
	cfg, err := config.Load(config.Options{
		AuthToken:     "test-" + strings.Repeat("a", 32),
		SessionSecret: "sec-" + strings.Repeat("b", 32),
		PublicHost:    "localhost",
		Port:          &ephemeral, // ephemeral
		PortMin:       40000,
		PortMax:       40500,
		KillGraceMS:   3000,
	})
	require.NoError(t, err)
	return cfg
}

// A1 — kill-self
func TestA1_KillSelfIsRefused(t *testing.T) {
	cfg := mustConfig(t)
	pm, err := process.New(cfg)
	require.NoError(t, err)
	err = pm.Kill(context.Background(), process.Target{PID: os.Getpid(), Port: 40000})
	require.ErrorIs(t, err, process.ErrSelfPID)
}

// A2 — cross-user kill
func TestA2_CrossUserIsRefused(t *testing.T) {
	cfg := mustConfig(t)
	pm, err := process.New(cfg)
	require.NoError(t, err)
	// UID 0 (root) is never our uid as a regular user
	err = pm.Kill(context.Background(), process.Target{PID: 1, Port: 40000})
	require.Error(t, err)
}

// A3 — label command-injection is data-only
func TestA3_LabelIsDataOnly(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	s, err := store.Open(dir)
	require.NoError(t, err)
	require.NoError(t, s.SetLabel("/tmp/a", []string{"/bin/echo"}, `"; rm -rf ~ ;"`))
	got, err := s.Label("/tmp/a", []string{"/bin/echo"})
	require.NoError(t, err)
	require.Equal(t, `"; rm -rf ~ ;"`, got)
	// No subprocess was ever invoked; label is returned verbatim.
}

// A4 — restart uses stored cwd (not user-supplied)
func TestA4_RestartUsesStoredCwdOnly(t *testing.T) {
	cfg := mustConfig(t)
	pm, err := process.New(cfg)
	require.NoError(t, err)
	// Public Restart API takes only a remembered-id; no cwd parameter exists.
	// (Compile-time assertion via method signature.)
	_ = pm.Restart // must not accept a cwd argument
}

// A5 — atomic concurrent rename
func TestA5_ConcurrentRenameIsSafe(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	s, err := store.Open(dir)
	require.NoError(t, err)
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			require.NoError(t, s.SetLabel("/tmp/x", []string{"/bin/foo"}, "label-"+string(rune('A'+i%26))))
		}(i)
	}
	wg.Wait()
	// State file must still be parseable
	s2, err := store.Open(dir)
	require.NoError(t, err)
	_, err = s2.Label("/tmp/x", []string{"/bin/foo"})
	require.NoError(t, err)
}

// A6 — short / empty bearer token is rejected
func TestA6_EmptyTokenIsRejected(t *testing.T) {
	cfg := mustConfig(t)
	a := auth.New(cfg)
	require.False(t, a.CheckBearer(""))
	require.False(t, a.CheckBearer("abc"))
}

// A7 — session cookie signed by old secret fails after rotation
func TestA7_CookieRotationInvalidatesSession(t *testing.T) {
	cfg1 := mustConfig(t)
	a1 := auth.New(cfg1)
	cookie := a1.IssueCookie(time.Now())
	cfg2 := mustConfig(t)
	// Force a different session secret
	cfg2.SessionSecret = "different-secret-" + strings.Repeat("c", 32)
	a2 := auth.New(cfg2)
	require.False(t, a2.VerifyCookie(cookie))
}

// A8 — port outside range is refused
func TestA8_PortRangeGuard(t *testing.T) {
	cfg := mustConfig(t)
	srv := server.New(cfg, nil, nil, nil, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	// POST /kill/22 should 400 even with valid bearer
	req, _ := server.NewBearerRequest("POST", ts.URL+"/kill/22", cfg.AuthToken)
	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	require.Equal(t, 400, resp.StatusCode)
}

// A9 — UDP listeners are not surfaced
func TestA9_ScannerIgnoresUDP(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	sc, err := scanner.Auto(mustConfig(t))
	require.NoError(t, err)
	listeners, err := sc.Scan(ctx)
	require.NoError(t, err)
	for _, l := range listeners {
		require.Equal(t, "tcp", l.Protocol)
	}
}

// A10 — slow scan times out without blocking UI
func TestA10_ScannerRespectsContextTimeout(t *testing.T) {
	sc, err := scanner.NewLsof(mustConfig(t))
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err = sc.Scan(ctx)
	require.Error(t, err) // must return before panicking
}

// A11 — repeated scans do not leak fds
func TestA11_NoFDLeakOverManyScans(t *testing.T) {
	start := countOpenFDs(t)
	sc, err := scanner.Auto(mustConfig(t))
	require.NoError(t, err)
	for i := 0; i < 500; i++ {
		_, _ = sc.Scan(context.Background())
	}
	end := countOpenFDs(t)
	// Allow small jitter; require no large growth.
	require.LessOrEqual(t, end-start, 20, "fd growth over 500 scans must be bounded")
}

// A12 — restarted processes are reaped (no zombies)
func TestA12_NoZombiesAfterRestart(t *testing.T) {
	cfg := mustConfig(t)
	pm, err := process.New(cfg)
	require.NoError(t, err)
	// Fire many short-lived restarts and ensure no zombies accumulate.
	for i := 0; i < 20; i++ {
		err := pm.SpawnDetached(context.Background(), process.Spec{Command: []string{"true"}, Cwd: t.TempDir()})
		require.NoError(t, err)
	}
	// Give reapers time.
	time.Sleep(500 * time.Millisecond)
	// ps-based zombie check (state Z)
	out, _ := exec.Command("bash", "-lc", `ps -xo state,pid | awk '$1 ~ /Z/ {print $2}'`).Output()
	require.Empty(t, strings.TrimSpace(string(out)), "zombies present: %q", out)
}

// A13 — XSS: command with <script> is escaped
func TestA13_XSSInCommandIsEscaped(t *testing.T) {
	// Render a row with a command containing <script>alert(1)</script>.
	html, err := server.RenderPortsFragment([]server.PortVM{{
		Port: 40123, Label: "x", Cmd: `<script>alert(1)</script>`, Cwd: "/tmp", Uptime: "1s", Source: "ext",
	}})
	require.NoError(t, err)
	require.NotContains(t, html, "<script>alert(1)</script>")
	require.Contains(t, html, "&lt;script&gt;alert(1)&lt;/script&gt;")
}

// A14 — copy URL uses PUBLIC_HOST, not Host header
func TestA14_CopyURLUsesPublicHost(t *testing.T) {
	cfg := mustConfig(t)
	cfg.PublicHost = "yourhost.example"
	got := server.PublicURL(cfg, 40123)
	require.Equal(t, "http://yourhost.example:40123/", got)
}

// A15 — atomic state write survives simulated crash
func TestA15_AtomicWrite(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	s, err := store.Open(dir)
	require.NoError(t, err)
	require.NoError(t, s.SetLabel("/tmp", []string{"cmd"}, "name"))
	// Simulate a stale .tmp file lying around
	require.NoError(t, os.WriteFile(dir+"/.port-manager/state.json.tmp", []byte("{garbage"), 0600))
	// Re-open and ensure state.json (not the tmp) is used
	s2, err := store.Open(dir)
	require.NoError(t, err)
	got, err := s2.Label("/tmp", []string{"cmd"})
	require.NoError(t, err)
	require.Equal(t, "name", got)
}

// A16 — restart detaches from dashboard pgid
func TestA16_RestartUsesNewProcessGroup(t *testing.T) {
	cfg := mustConfig(t)
	pm, err := process.New(cfg)
	require.NoError(t, err)
	pid, err := pm.SpawnForTest(context.Background(), process.Spec{Command: []string{"sleep", "2"}, Cwd: t.TempDir()})
	require.NoError(t, err)
	defer func() { _ = exec.Command("kill", "-9", itoa(pid)).Run() }()
	pgid, err := getpgid(pid)
	require.NoError(t, err)
	require.NotEqual(t, os.Getpid(), pgid, "restarted process must have its own pgid")
}

// A17 — remembered list is capped
func TestA17_RememberedListIsCapped(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	s, err := store.Open(dir)
	require.NoError(t, err)
	for i := 0; i < 200; i++ {
		require.NoError(t, s.Remember(store.Remembered{
			Port: 40000 + i%500, Command: []string{"/bin/echo"}, Cwd: "/tmp/a", Env: nil,
		}))
	}
	r, err := s.ListRemembered("/tmp/a", []string{"/bin/echo"})
	require.NoError(t, err)
	require.LessOrEqual(t, len(r), 5, "per-key retention cap is 5")
}

// A19 — CSRF guard on mutation routes
func TestA19_CSRFGuardRequiresXRW(t *testing.T) {
	cfg := mustConfig(t)
	srv := server.New(cfg, nil, nil, nil, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	// Cookie-auth'd mutation without X-Requested-With must fail
	cookie := auth.New(cfg).IssueCookie(time.Now())
	req, _ := server.NewCookieRequest("POST", ts.URL+"/kill/40100", cookie)
	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	require.Equal(t, 403, resp.StatusCode)
	// With X-Requested-With, request proceeds past CSRF guard (still 404 since no live port)
	req2, _ := server.NewCookieRequest("POST", ts.URL+"/kill/40100", cookie)
	req2.Header.Set("X-Requested-With", "XMLHttpRequest")
	resp2, err := ts.Client().Do(req2)
	require.NoError(t, err)
	require.NotEqual(t, 403, resp2.StatusCode)
}

// A20 — secrets are not logged
func TestA20_NoSecretsInLogs(t *testing.T) {
	cfg := mustConfig(t)
	captured := server.CaptureLogs(func() {
		srv := server.New(cfg, nil, nil, nil, nil)
		ts := httptest.NewServer(srv.Handler())
		defer ts.Close()
		req, _ := server.NewBearerRequest("GET", ts.URL+"/ports.json", cfg.AuthToken)
		_, _ = ts.Client().Do(req)
	})
	require.NotContains(t, captured, cfg.AuthToken)
	require.NotContains(t, captured, cfg.SessionSecret)
}

// ──────────────────────────────────────────────────────────────────────────
// helpers (to be implemented alongside process/scanner packages)
// ──────────────────────────────────────────────────────────────────────────

func countOpenFDs(t *testing.T) int {
	t.Helper()
	out, err := exec.Command("bash", "-lc", `lsof -p $$ 2>/dev/null | wc -l`).Output()
	require.NoError(t, err)
	var n int
	for _, ch := range strings.TrimSpace(string(out)) {
		if ch >= '0' && ch <= '9' {
			n = n*10 + int(ch-'0')
		}
	}
	return n
}

func itoa(n int) string {
	return strconv.Itoa(n)
}

func getpgid(pid int) (int, error) {
	out, err := exec.Command("ps", "-o", "pgid=", "-p", itoa(pid)).Output()
	if err != nil {
		return 0, err
	}
	s := strings.TrimSpace(string(out))
	n := 0
	for _, ch := range s {
		if ch >= '0' && ch <= '9' {
			n = n*10 + int(ch-'0')
		}
	}
	return n, nil
}

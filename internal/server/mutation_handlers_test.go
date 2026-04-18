package server_test

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/JeiKeiLim/my-home-index-server/internal/auth"
	"github.com/JeiKeiLim/my-home-index-server/internal/config"
	"github.com/JeiKeiLim/my-home-index-server/internal/inspector"
	"github.com/JeiKeiLim/my-home-index-server/internal/process"
	"github.com/JeiKeiLim/my-home-index-server/internal/scanner"
	"github.com/JeiKeiLim/my-home-index-server/internal/server"
	"github.com/JeiKeiLim/my-home-index-server/internal/store"
)

// fakeProcessManager implements server.ProcessManager for handler
// tests that need to observe Kill / Restart dispatches without
// actually signalling a real process. killErr / restartErr let a test
// force the error branch of the handler; killCalls / restartCalls let
// the test assert the dispatch happened exactly once with the
// expected argument.
type fakeProcessManager struct {
	mu           sync.Mutex
	killErr      error
	restartErr   error
	killCalls    []process.Target
	restartCalls []string
}

func (f *fakeProcessManager) Kill(_ context.Context, t process.Target) error {
	f.mu.Lock()
	f.killCalls = append(f.killCalls, t)
	err := f.killErr
	f.mu.Unlock()
	return err
}

func (f *fakeProcessManager) Restart(_ context.Context, id string) error {
	f.mu.Lock()
	f.restartCalls = append(f.restartCalls, id)
	err := f.restartErr
	f.mu.Unlock()
	return err
}

func (f *fakeProcessManager) RestartIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.restartCalls...)
}

func (f *fakeProcessManager) KillTargets() []process.Target {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]process.Target(nil), f.killCalls...)
}

// mutationCfg returns a config with an isolated state dir so the
// behavioural tests never touch a user's real ~/.port-manager.
func mutationCfg(t *testing.T) *config.Config {
	t.Helper()
	port := 0
	cfg, err := config.Load(config.Options{
		EnvFile:       t.TempDir() + "/.env",
		AuthToken:     testToken,
		SessionSecret: testSecret,
		PublicHost:    "localhost",
		Port:          &port,
		PortMin:       40000,
		PortMax:       40500,
		KillGraceMS:   500,
		StateDir:      t.TempDir(),
	})
	require.NoError(t, err)
	return cfg
}

// TestRenameMutationPersistsLabel verifies the full rename flow: a
// listener is resolved by port, inspect yields (cwd, command), the
// label lands in the store, and the handler returns the refreshed
// /ports fragment containing the new label.
func TestRenameMutationPersistsLabel(t *testing.T) {
	cfg := mutationCfg(t)
	insp := &fakeInspector{infos: map[int]*inspector.ProcInfo{
		4242: {PID: 4242, Command: []string{"node", "api.js"}, Cwd: "/srv/api", StartTime: time.Now()},
	}}
	sc := &fakeScanner{listeners: []scanner.Listener{
		{PID: 4242, Port: 40111, Protocol: "tcp", Addrs: []string{"0.0.0.0:40111"}, Source: "libproc"},
	}}
	st := mustOpenStore(t)

	srv := server.New(cfg, sc, inspector.Inspector(insp), st)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/rename/40111",
		strings.NewReader(url.Values{"label": {"staging-api"}}.Encode()))
	req.Header.Set("Authorization", "Bearer "+cfg.AuthToken)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	body, _ := io.ReadAll(resp.Body)
	html := string(body)
	require.Contains(t, html, "staging-api", "refreshed fragment must include new label")

	got, err := st.Label("/srv/api", []string{"node", "api.js"})
	require.NoError(t, err)
	require.Equal(t, "staging-api", got, "label must land in the store keyed on (cwd, command)")
}

// TestRenameMutationRejectsOverlongLabel asserts the 64-char ceiling
// from spec §5. The handler must 400 before touching the store.
func TestRenameMutationRejectsOverlongLabel(t *testing.T) {
	cfg := mutationCfg(t)
	sc := &fakeScanner{listeners: []scanner.Listener{{PID: 1, Port: 40200}}}
	insp := &fakeInspector{infos: map[int]*inspector.ProcInfo{
		1: {PID: 1, Command: []string{"x"}, Cwd: "/", StartTime: time.Now()},
	}}
	srv := server.New(cfg, sc, inspector.Inspector(insp), mustOpenStore(t))
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	form := url.Values{"label": {strings.Repeat("a", 65)}}
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/rename/40200", strings.NewReader(form.Encode()))
	req.Header.Set("Authorization", "Bearer "+cfg.AuthToken)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

// TestRenameMutationRejectsControlCharacter covers the "no control
// character" clause of the label invariant.
func TestRenameMutationRejectsControlCharacter(t *testing.T) {
	cfg := mutationCfg(t)
	sc := &fakeScanner{listeners: []scanner.Listener{{PID: 1, Port: 40201}}}
	insp := &fakeInspector{infos: map[int]*inspector.ProcInfo{
		1: {PID: 1, Command: []string{"x"}, Cwd: "/", StartTime: time.Now()},
	}}
	srv := server.New(cfg, sc, inspector.Inspector(insp), mustOpenStore(t))
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	form := url.Values{"label": {"hello\x1bworld"}}
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/rename/40201", strings.NewReader(form.Encode()))
	req.Header.Set("Authorization", "Bearer "+cfg.AuthToken)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

// TestRenameMutationAcceptsUnicodeLabel asserts non-ASCII labels
// within the 64-rune limit are accepted.
func TestRenameMutationAcceptsUnicodeLabel(t *testing.T) {
	cfg := mutationCfg(t)
	insp := &fakeInspector{infos: map[int]*inspector.ProcInfo{
		99: {PID: 99, Command: []string{"python3", "-m", "http.server"}, Cwd: "/tmp", StartTime: time.Now()},
	}}
	sc := &fakeScanner{listeners: []scanner.Listener{
		{PID: 99, Port: 40250, Protocol: "tcp", Addrs: []string{"[::1]:40250"}, Source: "libproc"},
	}}
	st := mustOpenStore(t)
	srv := server.New(cfg, sc, inspector.Inspector(insp), st)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	form := url.Values{"label": {"한국어-테스트 🚀"}}
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/rename/40250", strings.NewReader(form.Encode()))
	req.Header.Set("Authorization", "Bearer "+cfg.AuthToken)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	got, err := st.Label("/tmp", []string{"python3", "-m", "http.server"})
	require.NoError(t, err)
	require.Equal(t, "한국어-테스트 🚀", got)
}

// TestRenameMutationEmptyLabelClears verifies blank-label clears.
func TestRenameMutationEmptyLabelClears(t *testing.T) {
	cfg := mutationCfg(t)
	st := mustOpenStore(t)
	require.NoError(t, st.SetLabel("/tmp", []string{"python3"}, "to-be-cleared"))

	insp := &fakeInspector{infos: map[int]*inspector.ProcInfo{
		5: {PID: 5, Command: []string{"python3"}, Cwd: "/tmp", StartTime: time.Now()},
	}}
	sc := &fakeScanner{listeners: []scanner.Listener{{PID: 5, Port: 40260}}}
	srv := server.New(cfg, sc, inspector.Inspector(insp), st)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	form := url.Values{"label": {""}}
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/rename/40260", strings.NewReader(form.Encode()))
	req.Header.Set("Authorization", "Bearer "+cfg.AuthToken)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	got, err := st.Label("/tmp", []string{"python3"})
	require.NoError(t, err)
	require.Equal(t, "", got)
}

// TestRenameMutationPortOutOfRange bounces requests for ports
// outside the configured window with 400.
func TestRenameMutationPortOutOfRange(t *testing.T) {
	cfg := mutationCfg(t)
	srv := server.New(cfg)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/rename/22",
		strings.NewReader(url.Values{"label": {"x"}}.Encode()))
	req.Header.Set("Authorization", "Bearer "+cfg.AuthToken)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

// TestKillMutationRefusesSelfPID covers Iron Law 1: if the scanner
// surfaces the dashboard's own pid for a port in range, the handler
// must refuse before any SIGTERM is dispatched.
func TestKillMutationRefusesSelfPID(t *testing.T) {
	cfg := mutationCfg(t)
	sc := &fakeScanner{listeners: []scanner.Listener{
		{PID: os.Getpid(), Port: 40300, Protocol: "tcp"},
	}}
	pm, err := process.New(cfg)
	require.NoError(t, err)
	srv := server.New(cfg, sc, pm, mustOpenStore(t))
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/kill/40300", nil)
	req.Header.Set("Authorization", "Bearer "+cfg.AuthToken)
	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	require.True(t, isAlive(os.Getpid()), "dashboard must still be running")
}

// TestKillMutationEndToEnd spawns a real listener, calls POST /kill,
// and asserts both the process dies AND the store holds a Remembered
// entry for the killed port — exercising the env-snapshot-before-kill
// contract.
func TestKillMutationEndToEnd(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("signal semantics differ on this OS")
	}

	port := pickFreeListenerPort(t, 40401, 40499)
	cfg := mutationCfg(t)

	// Spawn a child that holds a TCP listener on the chosen port.
	// No SO_REUSEADDR — we want a hard bind error if the port got
	// taken between pickFreeListenerPort and child.Start, rather than
	// double-bind via SO_REUSEPORT semantics picking up a pre-existing
	// listener and masquerading as "our" child.
	script := "import socket,time,signal,sys\n" +
		"s=socket.socket()\n" +
		"s.bind(('127.0.0.1'," + strconv.Itoa(port) + ")); s.listen(1)\n" +
		"signal.signal(signal.SIGTERM, lambda *_: sys.exit(0))\n" +
		"print('bound'); sys.stdout.flush()\n" +
		"time.sleep(120)\n"
	child := exec.Command("python3", "-u", "-c", script)
	child.Env = append(os.Environ(), "PM_TEST_MARK=job9")
	child.Dir = t.TempDir()
	stdout, err := child.StdoutPipe()
	require.NoError(t, err)
	child.Stderr = os.Stderr
	require.NoError(t, child.Start())
	childPID := child.Process.Pid
	t.Cleanup(func() {
		_ = syscall.Kill(childPID, syscall.SIGKILL)
		_, _ = child.Process.Wait()
	})

	// Block until python prints "bound" so the listener is live
	// before we POST /kill.
	require.True(t, readLineWithTimeout(stdout, 3*time.Second), "python child never printed 'bound'")
	require.True(t, waitForListener(port, 3*time.Second), "child did not bind port %d in time", port)

	// Use the real scanner+inspector so the handler exercises the
	// same code path production does. Give the scanner a chance to
	// observe the child's LISTEN socket via libproc.
	sc, err := scanner.Auto(cfg)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		ls, err := sc.Scan(t.Context())
		if err != nil {
			return false
		}
		for _, l := range ls {
			if l.Port == port {
				return true
			}
		}
		return false
	}, 2*time.Second, 100*time.Millisecond, "scanner must see listener on port %d", port)
	insp := inspector.NewGopsutil()
	st := mustOpenStore(t)
	pm, err := process.New(cfg, process.WithStore(st))
	require.NoError(t, err)

	srv := server.New(cfg, sc, insp, pm, st)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/kill/"+strconv.Itoa(port), nil)
	req.Header.Set("Authorization", "Bearer "+cfg.AuthToken)
	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "kill must return 200 with updated fragment")

	// The listener goes away within the grace window. We detect its
	// exit by watching for the port to stop accepting connections.
	require.Eventually(t, func() bool {
		c, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), 100*time.Millisecond)
		if err != nil {
			return true
		}
		_ = c.Close()
		return false
	}, 3*time.Second, 100*time.Millisecond, "listener on port %d must stop accepting after kill", port)

	// The handler must have snapshot-and-persisted a Remembered entry
	// with the pre-kill env BEFORE calling Kill (Darwin's
	// KERN_PROCARGS2 strips envp once the pid exits). Assert a
	// remembered entry now exists for this port AND its Env carries
	// the PM_TEST_MARK=job9 marker we seeded into the child.
	all, err := st.AllRemembered()
	require.NoError(t, err)
	var match *store.Remembered
	for i := range all {
		if all[i].Port == port {
			entry := all[i]
			match = &entry
			break
		}
	}
	require.NotNil(t, match, "store.AllRemembered must contain entry for killed port %d", port)
	require.Contains(t, match.Env, "PM_TEST_MARK=job9",
		"Remembered.Env must carry pre-kill marker — snapshot happened after kill")

	// And ListRemembered(cwd, argv) keyed on the same inspector output
	// the handler used must return at least one entry — the
	// env-snapshot-before-kill contract is keyed per (cwd, command).
	byKey, err := st.ListRemembered(match.Cwd, match.Command)
	require.NoError(t, err)
	require.NotEmpty(t, byKey, "ListRemembered(cwd,cmd) must surface the new entry")
}

// TestKillMutationNotFoundWhenPortInactive covers the 404 branch:
// port in range but no live listener on it.
func TestKillMutationNotFoundWhenPortInactive(t *testing.T) {
	cfg := mutationCfg(t)
	sc := &fakeScanner{listeners: nil}
	pm, err := process.New(cfg)
	require.NoError(t, err)
	srv := server.New(cfg, sc, pm, mustOpenStore(t))
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/kill/40100", nil)
	req.Header.Set("Authorization", "Bearer "+cfg.AuthToken)
	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}

// TestRestartMutationSpawnsRememberedCommand asserts POST /restart/{id}
// dispatches exactly one process.Restart call with the expected
// remembered id. The fake ProcessManager records the call so the test
// observes an actual side effect, not just the 202 response code.
func TestRestartMutationSpawnsRememberedCommand(t *testing.T) {
	cfg := mutationCfg(t)
	st := mustOpenStore(t)
	rememberedCwd := t.TempDir()
	require.NoError(t, st.Remember(store.Remembered{
		Port:    40101,
		Command: []string{"/usr/bin/true"},
		Cwd:     rememberedCwd,
	}))
	entries, err := st.ListRemembered(rememberedCwd, []string{"/usr/bin/true"})
	require.NoError(t, err)
	require.NotEmpty(t, entries, "remembered entry must be persisted")
	id := entries[0].ID

	pm := &fakeProcessManager{}
	srv := server.New(cfg, server.ProcessManager(pm), st)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/restart/"+id, nil)
	req.Header.Set("Authorization", "Bearer "+cfg.AuthToken)
	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	require.Equalf(t, http.StatusAccepted, resp.StatusCode, "body=%s", string(body))

	// The handler must have called process.Restart exactly once with
	// the same id we POSTed. Observing the fake's side effect pins the
	// spawn dispatch, not just the 202 code.
	calls := pm.RestartIDs()
	require.Equal(t, []string{id}, calls,
		"process.Restart must be invoked exactly once with remembered id")
}

// TestRestartMutationMissingIDReturns404 confirms unknown remembered
// ids surface as 404, not 500.
func TestRestartMutationMissingIDReturns404(t *testing.T) {
	cfg := mutationCfg(t)
	st := mustOpenStore(t)
	pm, err := process.New(cfg, process.WithStore(st))
	require.NoError(t, err)
	srv := server.New(cfg, pm, st)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/restart/NOPE", nil)
	req.Header.Set("Authorization", "Bearer "+cfg.AuthToken)
	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}

// TestMutationRoutesCSRFGuard reasserts all three mutation routes
// reject cookie-authed requests without X-Requested-With. Bearer
// auth callers are exempt — they go through their own tests.
func TestMutationRoutesCSRFGuard(t *testing.T) {
	cfg := mutationCfg(t)
	srv := server.New(cfg)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	cookieVal := auth.New(cfg).IssueCookie(time.Now())
	routes := []string{"/kill/40100", "/restart/FAKE", "/rename/40100"}
	for _, r := range routes {
		req, _ := http.NewRequest(http.MethodPost, ts.URL+r, strings.NewReader("label=x"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(&http.Cookie{Name: "pm_session", Value: cookieVal})
		resp, err := ts.Client().Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		require.Equalf(t, http.StatusForbidden, resp.StatusCode, "%s must reject cookie auth without X-Requested-With", r)
	}
}

// TestRememberedListSurfacesRestartPendingRows verifies the
// /remembered endpoint returns rendered rows for every Remembered
// entry with the `restart-pending` class and a data-restart attribute
// keyed on the remembered id — the UI contract the Playwright S3
// "restart history" spec asserts against.
func TestRememberedListSurfacesRestartPendingRows(t *testing.T) {
	cfg := mutationCfg(t)
	st := mustOpenStore(t)
	require.NoError(t, st.Remember(store.Remembered{
		Port:    40150,
		Command: []string{"node", "worker.js"},
		Cwd:     "/srv/worker",
	}))

	srv := server.New(cfg, st)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/remembered", nil)
	req.Header.Set("Authorization", "Bearer "+cfg.AuthToken)
	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	body, _ := io.ReadAll(resp.Body)
	html := string(body)
	require.Contains(t, html, "restart-pending", "row must carry restart-pending class")
	require.Contains(t, html, "data-restart=", "row must carry data-restart id")
	require.Contains(t, html, "node worker.js", "command must render")
}

// TestKillMutationRememberedCapEndToEnd — A17, end-to-end. Six
// distinct listeners sharing the same (cwd, command) are each killed
// via POST /kill; the per-key retention cap (PerKeyRememberedCap = 5)
// must leave exactly five Remembered entries for that key.
func TestKillMutationRememberedCapEndToEnd(t *testing.T) {
	cfg := mutationCfg(t)
	st := mustOpenStore(t)

	sharedCwd := "/srv/worker"
	sharedCmd := []string{"node", "worker.js"}

	// Build six (pid, port) pairs — distinct pids so process.Target
	// bookkeeping treats them as separate calls, same (cwd, command)
	// so they all hash to one Remembered bucket.
	listeners := make([]scanner.Listener, 0, 6)
	infos := make(map[int]*inspector.ProcInfo, 6)
	for i := 0; i < 6; i++ {
		pid := 9000 + i
		port := 40410 + i
		listeners = append(listeners, scanner.Listener{PID: pid, Port: port, Protocol: "tcp"})
		infos[pid] = &inspector.ProcInfo{
			PID:       pid,
			Command:   sharedCmd,
			Cwd:       sharedCwd,
			StartTime: time.Now(),
		}
	}
	sc := &fakeScanner{listeners: listeners}
	insp := &fakeInspector{infos: infos}
	pm := &fakeProcessManager{}

	srv := server.New(cfg, sc, inspector.Inspector(insp), server.ProcessManager(pm), st)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	for _, l := range listeners {
		req, _ := http.NewRequest(http.MethodPost,
			ts.URL+"/kill/"+strconv.Itoa(l.Port), nil)
		req.Header.Set("Authorization", "Bearer "+cfg.AuthToken)
		resp, err := ts.Client().Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		require.Equalf(t, http.StatusOK, resp.StatusCode,
			"POST /kill/%d must succeed", l.Port)
	}

	// Six Kills dispatched, six Remembers attempted, five remain.
	require.Len(t, pm.KillTargets(), 6, "handler must dispatch Kill per POST")
	got, err := st.ListRemembered(sharedCwd, sharedCmd)
	require.NoError(t, err)
	require.Len(t, got, 5,
		"per-key retention cap is 5: 6 kills on same (cwd,cmd) must leave 5")
}

// TestKillMutationErrorDoesNotPersistRemembered pins the F1 fix:
// when process.Kill fails, the /ports fragment on the next scan must
// continue to show the live row (accurate — the kill didn't happen),
// which means the handler MUST NOT create a phantom Remembered entry
// for a still-live listener.
func TestKillMutationErrorDoesNotPersistRemembered(t *testing.T) {
	cfg := mutationCfg(t)
	st := mustOpenStore(t)

	const pid, port = 7777, 40333
	livingCwd := "/srv/still-alive"
	livingCmd := []string{"bash", "serve.sh"}

	sc := &fakeScanner{listeners: []scanner.Listener{
		{PID: pid, Port: port, Protocol: "tcp"},
	}}
	insp := &fakeInspector{infos: map[int]*inspector.ProcInfo{
		pid: {PID: pid, Command: livingCmd, Cwd: livingCwd, StartTime: time.Now()},
	}}
	pm := &fakeProcessManager{killErr: errors.New("boom: SIGTERM delivery failed")}

	srv := server.New(cfg, sc, inspector.Inspector(insp), server.ProcessManager(pm), st)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodPost,
		ts.URL+"/kill/"+strconv.Itoa(port), nil)
	req.Header.Set("Authorization", "Bearer "+cfg.AuthToken)
	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	// (a) handler surfaces the failure with 500.
	require.Equal(t, http.StatusInternalServerError, resp.StatusCode,
		"kill dispatch failure must surface as 500")

	// (b) no Remembered entry was persisted for the still-live listener.
	byKey, err := st.ListRemembered(livingCwd, livingCmd)
	require.NoError(t, err)
	require.Empty(t, byKey,
		"failed kill must NOT persist a phantom Remembered row")
	all, err := st.AllRemembered()
	require.NoError(t, err)
	require.Empty(t, all,
		"no Remembered entries anywhere — kill failed, nothing to restart")
}

// TestKillMutationBearerBypassesCSRF verifies the Authorization:
// Bearer path is exempt from the cookie-only X-Requested-With CSRF
// guard. Machine clients (curl, scripts) POST with bearer auth and no
// X-Requested-With header; a 403 there would break the documented
// bearer-auth workflow.
func TestKillMutationBearerBypassesCSRF(t *testing.T) {
	cfg := mutationCfg(t)
	const port = 40345
	sc := &fakeScanner{listeners: []scanner.Listener{
		{PID: 4242, Port: port, Protocol: "tcp"},
	}}
	insp := &fakeInspector{infos: map[int]*inspector.ProcInfo{
		4242: {PID: 4242, Command: []string{"node"}, Cwd: "/srv", StartTime: time.Now()},
	}}
	pm := &fakeProcessManager{}

	srv := server.New(cfg, sc, inspector.Inspector(insp),
		server.ProcessManager(pm), mustOpenStore(t))
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// NOTE: no X-Requested-With header. Bearer auth alone must suffice.
	req, _ := http.NewRequest(http.MethodPost,
		ts.URL+"/kill/"+strconv.Itoa(port), nil)
	req.Header.Set("Authorization", "Bearer "+cfg.AuthToken)
	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.NotEqual(t, http.StatusForbidden, resp.StatusCode,
		"bearer-auth POST must NOT be blocked by the CSRF guard")
	require.Equal(t, http.StatusOK, resp.StatusCode,
		"bearer-auth kill must reach the handler and return 200")
	require.Len(t, pm.KillTargets(), 1,
		"bearer-auth request must actually dispatch the Kill")
}

// TestRenameMutationAccepts64RuneUnicodeLabel — F2 boundary test at
// the HTTP layer. A 64-rune multibyte label whose byte length
// (4*64 = 256) blows past the old store-layer len() check must now
// 200 and land in the store.
func TestRenameMutationAccepts64RuneUnicodeLabel(t *testing.T) {
	cfg := mutationCfg(t)
	insp := &fakeInspector{infos: map[int]*inspector.ProcInfo{
		77: {PID: 77, Command: []string{"python3", "-m", "http.server"},
			Cwd: "/tmp/unicode", StartTime: time.Now()},
	}}
	sc := &fakeScanner{listeners: []scanner.Listener{
		{PID: 77, Port: 40275, Protocol: "tcp"},
	}}
	st := mustOpenStore(t)
	srv := server.New(cfg, sc, inspector.Inspector(insp), st)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	label := strings.Repeat("😀", store.MaxLabelLen) // 64 runes, 256 bytes
	require.Equal(t, store.MaxLabelLen, len([]rune(label)))
	require.Greater(t, len(label), store.MaxLabelLen,
		"sanity: byte length must exceed rune-count cap")

	form := url.Values{"label": {label}}
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/rename/40275",
		strings.NewReader(form.Encode()))
	req.Header.Set("Authorization", "Bearer "+cfg.AuthToken)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode,
		"64-rune multibyte label must be accepted end-to-end")

	got, err := st.Label("/tmp/unicode", []string{"python3", "-m", "http.server"})
	require.NoError(t, err)
	require.Equal(t, label, got, "label must persist verbatim")
}

// TestWritePortsFragmentDistinguishesTimeoutFromError — F3 fix.
// After a successful mutation the re-scan may fail; the handler must
// mirror handlePortsHTML and set X-Scan-Error: "error" for any
// non-timeout error (not "timeout" blindly).
func TestWritePortsFragmentDistinguishesTimeoutFromError(t *testing.T) {
	cfg := mutationCfg(t)
	st := mustOpenStore(t)
	const pid, port = 4321, 40288

	insp := &fakeInspector{infos: map[int]*inspector.ProcInfo{
		pid: {PID: pid, Command: []string{"node"}, Cwd: "/srv", StartTime: time.Now()},
	}}
	// Scanner succeeds on the pre-kill resolve. After the Kill call
	// the handler calls writePortsFragment → buildSnapshot, which
	// re-invokes Scan. Flip sc.err between the two phases by letting
	// the first call succeed and the second return a generic error.
	sc := &switchingScanner{
		ok: []scanner.Listener{{PID: pid, Port: port, Protocol: "tcp"}},
	}
	pm := &fakeProcessManager{}

	srv := server.New(cfg, scanner.Scanner(sc), inspector.Inspector(insp),
		server.ProcessManager(pm), st)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Flip the scanner into error mode AFTER the pre-kill resolve
	// returns successfully by arming the trip count.
	sc.errAfter = 1
	sc.err = errors.New("lsof exploded")

	req, _ := http.NewRequest(http.MethodPost,
		ts.URL+"/kill/"+strconv.Itoa(port), nil)
	req.Header.Set("Authorization", "Bearer "+cfg.AuthToken)
	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode,
		"kill still succeeds even if refresh scan fails")
	require.Equal(t, "error", resp.Header.Get("X-Scan-Error"),
		"non-timeout scan failure must surface as X-Scan-Error: error, not timeout")
}

// switchingScanner returns a canned listener slice until it has been
// called errAfter times, then flips to returning err. Used to exercise
// the post-mutation rescan error path without timing games.
type switchingScanner struct {
	mu       sync.Mutex
	ok       []scanner.Listener
	calls    int
	errAfter int
	err      error
}

func (s *switchingScanner) Name() string { return "switching" }
func (s *switchingScanner) Scan(_ context.Context) ([]scanner.Listener, error) {
	s.mu.Lock()
	s.calls++
	calls := s.calls
	errAfter := s.errAfter
	err := s.err
	s.mu.Unlock()
	if err != nil && calls > errAfter {
		return nil, err
	}
	return s.ok, nil
}

// ──────────────────────────────────────────────────────────────────
// helpers
// ──────────────────────────────────────────────────────────────────

func mustOpenStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(t.TempDir())
	require.NoError(t, err)
	return s
}

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

func pickFreeListenerPort(t *testing.T, lo, hi int) int {
	t.Helper()
	for p := lo; p <= hi; p++ {
		ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(p))
		if err != nil {
			continue
		}
		_ = ln.Close()
		return p
	}
	t.Fatalf("no free port in [%d,%d]", lo, hi)
	return 0
}

func waitForListener(port int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), 100*time.Millisecond)
		if err == nil {
			_ = c.Close()
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// readLineWithTimeout reads a single line from r. Returns true if a
// line arrived before timeout, false otherwise.
func readLineWithTimeout(r io.Reader, timeout time.Duration) bool {
	done := make(chan bool, 1)
	go func() {
		buf := make([]byte, 64)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				done <- true
				return
			}
			if err != nil {
				done <- false
				return
			}
		}
	}()
	select {
	case ok := <-done:
		return ok
	case <-time.After(timeout):
		return false
	}
}

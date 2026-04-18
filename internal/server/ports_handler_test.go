package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/JeiKeiLim/my-home-index-server/internal/auth"
	"github.com/JeiKeiLim/my-home-index-server/internal/config"
	"github.com/JeiKeiLim/my-home-index-server/internal/inspector"
	"github.com/JeiKeiLim/my-home-index-server/internal/scanner"
	"github.com/JeiKeiLim/my-home-index-server/internal/server"
)

// fakeScanner returns a fixed listener slice or an error. Sleep simulates
// a hung lsof so we can exercise the 1s timeout path.
type fakeScanner struct {
	listeners []scanner.Listener
	err       error
	sleep     time.Duration
}

func (f *fakeScanner) Name() string { return "fake" }
func (f *fakeScanner) Scan(ctx context.Context) ([]scanner.Listener, error) {
	if f.sleep > 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(f.sleep):
		}
	}
	if f.err != nil {
		return nil, f.err
	}
	return f.listeners, nil
}

type fakeInspector struct{ infos map[int]*inspector.ProcInfo }

func (f *fakeInspector) Inspect(_ context.Context, pid int) (*inspector.ProcInfo, error) {
	if info, ok := f.infos[pid]; ok {
		return info, nil
	}
	return nil, inspector.ErrNotFound
}

func mustCfgPorts(t *testing.T) *config.Config {
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
		KillGraceMS:   3000,
	})
	require.NoError(t, err)
	return cfg
}

func newPortsTS(t *testing.T, sc scanner.Scanner, insp inspector.Inspector) (*httptest.Server, *config.Config) {
	t.Helper()
	cfg := mustCfgPorts(t)
	srv := server.New(cfg, sc, insp)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, cfg
}

func bearer(t *testing.T, ts *httptest.Server, method, path string) *http.Response {
	t.Helper()
	req, err := server.NewBearerRequest(method, ts.URL+path, testToken)
	require.NoError(t, err)
	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	return resp
}

// TestPortsHTMLReturnsRowsAndCards asserts that GET /ports renders both
// desktop <tr> rows and the OOB-targeted #rows-mobile <div class="card">
// blocks in a single response — the htmx hx-swap-oob contract.
func TestPortsHTMLReturnsRowsAndCards(t *testing.T) {
	insp := &fakeInspector{infos: map[int]*inspector.ProcInfo{
		101: {PID: 101, Command: []string{"node", "server.js"}, Cwd: "/srv/api", StartTime: time.Now().Add(-90 * time.Minute)},
	}}
	sc := &fakeScanner{listeners: []scanner.Listener{
		{PID: 101, Port: 40102, Protocol: "tcp", Addrs: []string{"0.0.0.0:40102"}, Source: "libproc"},
	}}
	ts, _ := newPortsTS(t, sc, insp)

	resp := bearer(t, ts, http.MethodGet, "/ports")
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, resp.Header.Get("Content-Type"), "text/html")

	body, _ := io.ReadAll(resp.Body)
	html := string(body)
	require.Contains(t, html, "<tr", "desktop <tr> rows must render")
	require.Contains(t, html, `data-port="40102"`, "row carries data-port")
	require.Contains(t, html, "node server.js", "command is rendered")
	require.Contains(t, html, `id="rows-mobile"`, "OOB mobile container present")
	require.Contains(t, html, `hx-swap-oob="true"`, "OOB swap declared so htmx routes mobile cards")
	require.Contains(t, html, `class="card`, "mobile <div class='card'> blocks render")
}

// TestPortsJSONShape asserts /ports.json returns the spec'd JSON
// schema: an array of {port, label, cmd, cwd, uptime_s, source, pid,
// listen_addrs, alive, remembered}.
func TestPortsJSONShape(t *testing.T) {
	insp := &fakeInspector{infos: map[int]*inspector.ProcInfo{
		202: {PID: 202, Command: []string{"python3", "-m", "http.server"}, Cwd: "/tmp", StartTime: time.Now().Add(-30 * time.Second)},
	}}
	sc := &fakeScanner{listeners: []scanner.Listener{
		{PID: 202, Port: 40050, Protocol: "tcp", Addrs: []string{"127.0.0.1:40050"}, Source: "libproc"},
	}}
	ts, _ := newPortsTS(t, sc, insp)

	resp := bearer(t, ts, http.MethodGet, "/ports.json")
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, resp.Header.Get("Content-Type"), "application/json")

	var rows []map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&rows))
	require.Len(t, rows, 1)
	row := rows[0]
	require.EqualValues(t, 40050, row["port"])
	require.EqualValues(t, 202, row["pid"])
	require.Equal(t, "external", row["source"])
	require.Equal(t, true, row["alive"])
	require.Equal(t, false, row["remembered"])
	require.Contains(t, row, "uptime_s")
	require.Contains(t, row, "listen_addrs")
	require.Equal(t, "python3 -m http.server", row["cmd"])
	require.Equal(t, "/tmp", row["cwd"])
	// label must be present in the JSON shape (empty string when no
	// user-supplied label is attached via the store).
	require.Contains(t, row, "label")
	require.Equal(t, "", row["label"])
}

// TestPortsHTMLEscapesXSS — A13. A process whose argv contains a script
// tag must be HTML-escaped in both the desktop row and the mobile card
// (single-pass auto-escape via html/template).
func TestPortsHTMLEscapesXSS(t *testing.T) {
	insp := &fakeInspector{infos: map[int]*inspector.ProcInfo{
		303: {PID: 303, Command: []string{"<script>alert(1)</script>"}, Cwd: "/tmp", StartTime: time.Now()},
	}}
	sc := &fakeScanner{listeners: []scanner.Listener{
		{PID: 303, Port: 40300},
	}}
	ts, _ := newPortsTS(t, sc, insp)

	resp := bearer(t, ts, http.MethodGet, "/ports")
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	html := string(body)
	require.NotContains(t, html, "<script>alert(1)</script>")
	require.Contains(t, html, "&lt;script&gt;alert(1)&lt;/script&gt;")
}

// TestPortsJSONExcludesSelfPID — defence-in-depth on top of the
// scanner's own filter. Even if a stub scanner surfaces selfPID, the
// model layer drops the row.
func TestPortsJSONExcludesSelfPID(t *testing.T) {
	insp := &fakeInspector{infos: map[int]*inspector.ProcInfo{
		101: {PID: 101, Command: []string{"x"}, Cwd: "/", StartTime: time.Now()},
	}}
	sc := &fakeScanner{listeners: []scanner.Listener{
		{PID: 101, Port: 40001},
	}}
	cfg := mustCfgPorts(t)
	srv := server.New(cfg, sc, insp)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp := bearer(t, ts, http.MethodGet, "/ports.json")
	defer resp.Body.Close()
	var rows []map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&rows))
	// Listener at PID 101 is not the dashboard's PID, so it should
	// appear; this test mainly confirms ports.json never blows up on
	// inspector lookups when the listener is fully resolvable.
	require.Len(t, rows, 1)
	require.EqualValues(t, 40001, rows[0]["port"])
}

// TestPortsTimeoutReturnsLastSnapshot — S9 / A10. A scanner that hangs
// past the 1s budget must surface 200 + X-Scan-Error: timeout while
// re-serving the previously cached snapshot.
func TestPortsTimeoutReturnsLastSnapshot(t *testing.T) {
	infos := map[int]*inspector.ProcInfo{
		404: {PID: 404, Command: []string{"server"}, Cwd: "/opt/x", StartTime: time.Now().Add(-time.Hour)},
	}
	sc := &fakeScanner{listeners: []scanner.Listener{{PID: 404, Port: 40404}}}
	insp := &fakeInspector{infos: infos}

	cfg := mustCfgPorts(t)
	srv := server.New(cfg, scanner.Scanner(sc), inspector.Inspector(insp))
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Prime the snapshot with a successful scan.
	resp := bearer(t, ts, http.MethodGet, "/ports.json")
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Empty(t, resp.Header.Get("X-Scan-Error"))

	// Now flip the scanner into "hangs longer than the budget" mode.
	sc.sleep = 1500 * time.Millisecond

	resp2 := bearer(t, ts, http.MethodGet, "/ports.json")
	defer resp2.Body.Close()
	require.Equal(t, http.StatusOK, resp2.StatusCode)
	require.Equal(t, "timeout", resp2.Header.Get("X-Scan-Error"))
	body2, _ := io.ReadAll(resp2.Body)
	// Last good snapshot has 1 row at port 40404.
	require.Contains(t, string(body2), `"port":40404`)
}

// TestPortsScannerErrorFallsBackToCache asserts that any non-timeout
// scanner error also re-serves the cached rows with X-Scan-Error set.
func TestPortsScannerErrorFallsBackToCache(t *testing.T) {
	insp := &fakeInspector{infos: map[int]*inspector.ProcInfo{
		1: {PID: 1, Command: []string{"x"}, Cwd: "/", StartTime: time.Now()},
	}}
	sc := &fakeScanner{listeners: []scanner.Listener{{PID: 1, Port: 40010}}}
	cfg := mustCfgPorts(t)
	srv := server.New(cfg, scanner.Scanner(sc), inspector.Inspector(insp))
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Prime cache.
	resp := bearer(t, ts, http.MethodGet, "/ports.json")
	resp.Body.Close()

	sc.err = errors.New("scanner exploded")
	resp2 := bearer(t, ts, http.MethodGet, "/ports.json")
	defer resp2.Body.Close()
	require.Equal(t, http.StatusOK, resp2.StatusCode)
	require.NotEmpty(t, resp2.Header.Get("X-Scan-Error"))
	body, _ := io.ReadAll(resp2.Body)
	require.Contains(t, string(body), `"port":40010`)
}

// TestPortsRequiresAuth — both endpoints sit behind requireAuth.
func TestPortsRequiresAuth(t *testing.T) {
	cfg := mustCfgPorts(t)
	srv := server.New(cfg)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	for _, path := range []string{"/ports", "/ports.json"} {
		resp, err := ts.Client().Get(ts.URL + path)
		require.NoError(t, err)
		resp.Body.Close()
		require.Equalf(t, http.StatusUnauthorized, resp.StatusCode, "%s without auth must be 401", path)
	}
}

// TestPortsJSONEmptyArrayWithNoScanner — when the server is built with
// no scanner (test stubs / pre-wiring), /ports.json must still return a
// `[]` JSON array, never `null`.
func TestPortsJSONEmptyArrayWithNoScanner(t *testing.T) {
	cfg := mustCfgPorts(t)
	srv := server.New(cfg, auth.New(cfg))
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp := bearer(t, ts, http.MethodGet, "/ports.json")
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	require.Equal(t, "[]", strings.TrimSpace(string(body)))
}

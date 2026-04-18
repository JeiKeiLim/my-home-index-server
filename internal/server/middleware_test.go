package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRedactCookieHeaderPreservesWhitespace pins the redactor's output
// format byte-for-byte. Regressions in whitespace handling (e.g. a
// hard-coded leading space in the replacement) would silently make
// golden-log comparisons unstable, so the test covers pm_session in
// first, middle and last positions.
func TestRedactCookieHeaderPreservesWhitespace(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"pm_session=abc; other=x", "pm_session=[REDACTED]; other=x"},
		{"other=x; pm_session=abc", "other=x; pm_session=[REDACTED]"},
		{"a=1; pm_session=abc; b=2", "a=1; pm_session=[REDACTED]; b=2"},
		{"pm_session=abc", "pm_session=[REDACTED]"},
		{"other=x", "other=x"},
		{"", ""},
	}
	for _, c := range cases {
		got := redactCookieHeader(c.in)
		require.Equalf(t, c.want, got, "input %q", c.in)
	}
}

// TestRecoverMiddlewareTranslatesPanicTo500 asserts a handler panic
// becomes a 500 response (logged, not propagated to the goroutine) and
// that the middleware stays usable for the next request. The second
// request — served by the same handler instance — must also return
// 500 rather than leaving the connection in an undefined state.
func TestRecoverMiddlewareTranslatesPanicTo500(t *testing.T) {
	calls := 0
	h := recoverMiddleware(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		calls++
		panic("boom")
	}))

	rec1 := httptest.NewRecorder()
	h.ServeHTTP(rec1, httptest.NewRequest(http.MethodGet, "/x", nil))
	require.Equal(t, http.StatusInternalServerError, rec1.Code)

	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/x", nil))
	require.Equal(t, http.StatusInternalServerError, rec2.Code)
	require.Equal(t, 2, calls, "middleware must keep dispatching after a recovered panic")
}

// TestRemoteIP_TrustsXFFOnlyWhenEnabled covers the rate-limit-bucket
// helper: with trustXFF=false the function must always fall back to
// r.RemoteAddr (so a spoofed XFF cannot escape the per-IP limit on a
// naked HTTP listener); with trustXFF=true it picks the first
// non-empty XFF segment, stripping quotes and surrounding whitespace.
func TestRemoteIP_TrustsXFFOnlyWhenEnabled(t *testing.T) {
	cases := []struct {
		name     string
		xff      string
		remote   string
		trust    bool
		wantKey  string
	}{
		{"no xff, no trust", "", "1.2.3.4:5555", false, "1.2.3.4"},
		{"xff present, no trust", "9.9.9.9", "1.2.3.4:5555", false, "1.2.3.4"},
		{"xff single hop, trust", "9.9.9.9", "1.2.3.4:5555", true, "9.9.9.9"},
		{"xff multi hop, trust picks first", "9.9.9.9, 10.0.0.1", "1.2.3.4:5555", true, "9.9.9.9"},
		{"xff with whitespace, trust trims", "  9.9.9.9  , 10.0.0.1", "1.2.3.4:5555", true, "9.9.9.9"},
		{"xff quoted ipv6, trust strips quotes", `"[::1]"`, "1.2.3.4:5555", true, "[::1]"},
		{"empty leading segment, trust skips to next", ", 10.0.0.1", "1.2.3.4:5555", true, "10.0.0.1"},
		{"all empty xff, trust falls back to remote", " , , ", "1.2.3.4:5555", true, "1.2.3.4"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/login", nil)
			req.RemoteAddr = c.remote
			if c.xff != "" {
				req.Header.Set("X-Forwarded-For", c.xff)
			}
			require.Equal(t, c.wantKey, remoteIP(req, c.trust))
		})
	}
}

// TestRecoverMiddlewareAfterRequestIDLogsTraceID confirms the recover
// middleware, when composed after requestIDMiddleware (as in buildHandler),
// still emits the 500 and the X-Request-Id header is set on the way
// out. This pins the contract that panics never short-circuit the
// response header plumbing.
func TestRecoverMiddlewareAfterRequestIDLogsTraceID(t *testing.T) {
	h := requestIDMiddleware(recoverMiddleware(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		panic("boom")
	})))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	require.Equal(t, http.StatusInternalServerError, rec.Code)
	require.NotEmpty(t, rec.Header().Get("X-Request-Id"))
}

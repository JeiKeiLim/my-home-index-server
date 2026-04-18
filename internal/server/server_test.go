package server_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/JeiKeiLim/my-home-index-server/internal/auth"
	"github.com/JeiKeiLim/my-home-index-server/internal/config"
	"github.com/JeiKeiLim/my-home-index-server/internal/server"
)

const (
	testToken  = "tok-abcdefghijklmnopqrstuvwxyz0123"
	testSecret = "sec-abcdefghijklmnopqrstuvwxyz0123"
)

func mustCfg(t *testing.T) *config.Config {
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

func newTS(t *testing.T) (*httptest.Server, *config.Config) {
	t.Helper()
	cfg := mustCfg(t)
	srv := server.New(cfg)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, cfg
}

// nonRedirectClient strips the default 10-redirect-follow behaviour so
// tests can assert on the 303 status itself instead of the final
// destination.
func nonRedirectClient(t *testing.T, ts *httptest.Server) *http.Client {
	t.Helper()
	c := *ts.Client()
	c.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &c
}

func bodyString(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return string(b)
}

func TestHealthzReturnsOK(t *testing.T) {
	ts, _ := newTS(t)
	resp, err := ts.Client().Get(ts.URL + "/healthz")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "ok", bodyString(t, resp))
}

func TestUnauthedRootRedirectsToLogin(t *testing.T) {
	ts, _ := newTS(t)
	c := nonRedirectClient(t, ts)
	resp, err := c.Get(ts.URL + "/")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Equal(t, "/login", resp.Header.Get("Location"))
}

func TestLoginGetRendersForm(t *testing.T) {
	ts, _ := newTS(t)
	resp, err := ts.Client().Get(ts.URL + "/login")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body := bodyString(t, resp)
	require.Contains(t, body, `name="token"`)
	require.Contains(t, body, `action="/login"`)
	require.Contains(t, body, `method="POST"`)
}

func TestLoginPostSetsCookieAndRedirects(t *testing.T) {
	ts, _ := newTS(t)
	c := nonRedirectClient(t, ts)
	form := url.Values{"token": {testToken}}
	resp, err := c.PostForm(ts.URL+"/login", form)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Equal(t, "/", resp.Header.Get("Location"))
	cookies := resp.Cookies()
	var session *http.Cookie
	for _, ck := range cookies {
		if ck.Name == "pm_session" {
			session = ck
		}
	}
	require.NotNil(t, session, "expected pm_session cookie")
	require.NotEmpty(t, session.Value)
	require.True(t, session.HttpOnly)
}

func TestLoginPostBadTokenReturns401AndNoCookie(t *testing.T) {
	ts, _ := newTS(t)
	c := nonRedirectClient(t, ts)
	resp, err := c.PostForm(ts.URL+"/login", url.Values{"token": {"wrong"}})
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	for _, ck := range resp.Cookies() {
		require.NotEqual(t, "pm_session", ck.Name, "no cookie on bad creds")
	}
	require.Contains(t, bodyString(t, resp), "invalid token")
}

func TestLoginThenDashboardRendersShell(t *testing.T) {
	ts, _ := newTS(t)
	c := nonRedirectClient(t, ts)
	resp, err := c.PostForm(ts.URL+"/login", url.Values{"token": {testToken}})
	require.NoError(t, err)
	resp.Body.Close()
	var session *http.Cookie
	for _, ck := range resp.Cookies() {
		if ck.Name == "pm_session" {
			session = ck
		}
	}
	require.NotNil(t, session)

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/", nil)
	require.NoError(t, err)
	req.AddCookie(session)
	resp2, err := ts.Client().Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp2.StatusCode)
	body := bodyString(t, resp2)
	require.Contains(t, body, `id="ports-body"`)
	require.Contains(t, body, `hx-get="/ports"`)
	require.Contains(t, body, `hx-trigger="load, every 2s"`)
	require.Contains(t, body, `localhost`)
}

func TestLogoutClearsCookie(t *testing.T) {
	ts, cfg := newTS(t)
	c := nonRedirectClient(t, ts)
	cookieVal := auth.New(cfg).IssueCookie(time.Now())
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/logout", nil)
	require.NoError(t, err)
	req.AddCookie(&http.Cookie{Name: "pm_session", Value: cookieVal})
	// htmx attaches X-Requested-With on hx-post; /logout is now a
	// CSRF-guarded mutation so the header is required for cookie auth.
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	resp, err := c.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Equal(t, "/login", resp.Header.Get("Location"))
	require.Equal(t, "/login", resp.Header.Get("HX-Redirect"))
	cleared := false
	for _, ck := range resp.Cookies() {
		if ck.Name == "pm_session" && ck.MaxAge < 0 {
			cleared = true
		}
	}
	require.True(t, cleared, "expected pm_session to be expired")
}

// TestLogoutRequiresCSRF asserts that POST /logout authenticated by a
// session cookie is rejected with 403 unless the request carries the
// X-Requested-With: XMLHttpRequest header that htmx sets automatically.
// With the header present, logout succeeds (303 → /login). Bearer-auth
// callers bypass the CSRF check — that branch is covered separately.
func TestLogoutRequiresCSRF(t *testing.T) {
	ts, cfg := newTS(t)
	c := nonRedirectClient(t, ts)
	cookieVal := auth.New(cfg).IssueCookie(time.Now())

	// Without X-Requested-With → 403.
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/logout", nil)
	require.NoError(t, err)
	req.AddCookie(&http.Cookie{Name: "pm_session", Value: cookieVal})
	resp, err := c.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusForbidden, resp.StatusCode)

	// With X-Requested-With → 303.
	req2, err := http.NewRequest(http.MethodPost, ts.URL+"/logout", nil)
	require.NoError(t, err)
	req2.AddCookie(&http.Cookie{Name: "pm_session", Value: cookieVal})
	req2.Header.Set("X-Requested-With", "XMLHttpRequest")
	resp2, err := c.Do(req2)
	require.NoError(t, err)
	resp2.Body.Close()
	require.Equal(t, http.StatusSeeOther, resp2.StatusCode)
	require.Equal(t, "/login", resp2.Header.Get("Location"))
}

// TestLogoutBearerBypassCSRF asserts scripted callers using bearer
// auth can sign out without the XHR header — matches the /kill/{port}
// behaviour where bearer auth is CSRF-exempt.
func TestLogoutBearerBypassCSRF(t *testing.T) {
	ts, cfg := newTS(t)
	c := nonRedirectClient(t, ts)
	req, _ := server.NewBearerRequest(http.MethodPost, ts.URL+"/logout", cfg.AuthToken)
	resp, err := c.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Equal(t, "/login", resp.Header.Get("Location"))
}

// TestStaticDirectoryListingDisabled confirms a trailing-slash request
// under /static/ returns 404 rather than an auto-generated directory
// index enumerating every embedded asset.
func TestStaticDirectoryListingDisabled(t *testing.T) {
	ts, _ := newTS(t)
	resp, err := ts.Client().Get(ts.URL + "/static/")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}

// TestXRequestIDHeaderPresent asserts RequestID middleware stamps the
// X-Request-Id response header on every response, including public
// routes like /healthz and redirect responses like unauth GET /.
func TestXRequestIDHeaderPresent(t *testing.T) {
	ts, _ := newTS(t)

	resp, err := ts.Client().Get(ts.URL + "/healthz")
	require.NoError(t, err)
	resp.Body.Close()
	require.NotEmpty(t, resp.Header.Get("X-Request-Id"), "/healthz must carry X-Request-Id")

	c := nonRedirectClient(t, ts)
	resp2, err := c.Get(ts.URL + "/")
	require.NoError(t, err)
	resp2.Body.Close()
	require.NotEmpty(t, resp2.Header.Get("X-Request-Id"), "redirect responses must carry X-Request-Id")
}

// TestAuth_RateLimitUsesXFFWhenTrusted pins the test-isolation fix:
// when TrustXFF=true, the per-IP rate-limit bucket is derived from the
// first X-Forwarded-For hop, not r.RemoteAddr. Two different XFF
// values therefore have independent buckets, and exhausting one
// bucket must NOT block a request that arrives with a different XFF
// from the same loopback peer.
func TestAuth_RateLimitUsesXFFWhenTrusted(t *testing.T) {
	cfg := mustCfg(t)
	cfg.TrustXFF = true
	srv := server.New(cfg)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	c := nonRedirectClient(t, ts)

	post := func(token, xff string) *http.Response {
		req, err := http.NewRequest(http.MethodPost, ts.URL+"/login",
			strings.NewReader("token="+token))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("X-Forwarded-For", xff)
		resp, err := c.Do(req)
		require.NoError(t, err)
		return resp
	}

	for i := 0; i < auth.RateLimitCapacity; i++ {
		resp := post("wrong", "alpha")
		resp.Body.Close()
	}

	resp := post(testToken, "alpha")
	resp.Body.Close()
	require.Equal(t, http.StatusTooManyRequests, resp.StatusCode,
		"alpha bucket must be exhausted after 5 failures")

	resp2 := post(testToken, "beta")
	resp2.Body.Close()
	require.Equal(t, http.StatusSeeOther, resp2.StatusCode,
		"beta has its own bucket and must succeed")
}

// TestAuth_RateLimitIgnoresXFFWhenNotTrusted asserts the safe default:
// with TrustXFF=false, rotating X-Forwarded-For does NOT let a caller
// escape rate-limiting. The bucket key is r.RemoteAddr (loopback in
// httptest) regardless of any XFF header an attacker injects.
func TestAuth_RateLimitIgnoresXFFWhenNotTrusted(t *testing.T) {
	ts, _ := newTS(t)
	c := nonRedirectClient(t, ts)

	post := func(token, xff string) *http.Response {
		req, err := http.NewRequest(http.MethodPost, ts.URL+"/login",
			strings.NewReader("token="+token))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if xff != "" {
			req.Header.Set("X-Forwarded-For", xff)
		}
		resp, err := c.Do(req)
		require.NoError(t, err)
		return resp
	}

	for i := 0; i < auth.RateLimitCapacity; i++ {
		resp := post("wrong", "alpha")
		resp.Body.Close()
	}

	resp := post(testToken, "beta")
	resp.Body.Close()
	require.Equal(t, http.StatusTooManyRequests, resp.StatusCode,
		"XFF must be ignored — RemoteAddr-keyed bucket is shared across XFF values")
}

func TestRateLimitOnLoginReturns429WithRetryAfter(t *testing.T) {
	ts, _ := newTS(t)
	c := nonRedirectClient(t, ts)
	for i := 0; i < auth.RateLimitCapacity; i++ {
		resp, err := c.PostForm(ts.URL+"/login", url.Values{"token": {"wrong"}})
		require.NoError(t, err)
		resp.Body.Close()
	}
	resp, err := c.PostForm(ts.URL+"/login", url.Values{"token": {testToken}})
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
	require.NotEmpty(t, resp.Header.Get("Retry-After"))
}

func TestKillUnauthedReturns401(t *testing.T) {
	ts, _ := newTS(t)
	resp, err := ts.Client().Post(ts.URL+"/kill/40190", "", nil)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestKillWithBadBearerReturns401(t *testing.T) {
	ts, _ := newTS(t)
	req, _ := server.NewBearerRequest(http.MethodPost, ts.URL+"/kill/40190", "wrong-token")
	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestKillCSRFGuardOnCookieAuth(t *testing.T) {
	ts, cfg := newTS(t)
	cookieVal := auth.New(cfg).IssueCookie(time.Now())
	// Without X-Requested-With → 403 (CSRF)
	req, _ := server.NewCookieRequest(http.MethodPost, ts.URL+"/kill/40100", cookieVal)
	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	// With X-Requested-With → not 403 (passes CSRF; downstream handler
	// returns 501 since the process manager isn't wired).
	req2, _ := server.NewCookieRequest(http.MethodPost, ts.URL+"/kill/40100", cookieVal)
	req2.Header.Set("X-Requested-With", "XMLHttpRequest")
	resp2, err := ts.Client().Do(req2)
	require.NoError(t, err)
	resp2.Body.Close()
	require.NotEqual(t, http.StatusForbidden, resp2.StatusCode)
}

func TestKillRangeGuard(t *testing.T) {
	ts, cfg := newTS(t)
	req, _ := server.NewBearerRequest(http.MethodPost, ts.URL+"/kill/22", cfg.AuthToken)
	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestStaticAssetsServed(t *testing.T) {
	ts, _ := newTS(t)
	resp, err := ts.Client().Get(ts.URL + "/static/style.css")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body := bodyString(t, resp)
	require.Contains(t, body, "--accent")
	resp2, err := ts.Client().Get(ts.URL + "/static/htmx.min.js")
	require.NoError(t, err)
	defer resp2.Body.Close()
	require.Equal(t, http.StatusOK, resp2.StatusCode)
}

func TestLoggerRedactsAuthorizationAndCookie(t *testing.T) {
	ts, cfg := newTS(t)
	captured := server.CaptureLogs(func() {
		req, _ := server.NewBearerRequest(http.MethodGet, ts.URL+"/ports.json", cfg.AuthToken)
		resp, err := ts.Client().Do(req)
		require.NoError(t, err)
		resp.Body.Close()
	})
	require.NotEmpty(t, captured, "expected at least one log line")
	require.NotContains(t, captured, cfg.AuthToken, "AUTH_TOKEN must be redacted")
	require.NotContains(t, captured, cfg.SessionSecret, "SESSION_SECRET must never be logged")
	require.Contains(t, captured, "Bearer [REDACTED]")
}

func TestPublicURLUsesConfigHost(t *testing.T) {
	cfg := mustCfg(t)
	cfg.PublicHost = "yourhost.example"
	got := server.PublicURL(cfg, 40123)
	require.Equal(t, "http://yourhost.example:40123/", got)
}

func TestRenderPortsFragmentEscapesXSS(t *testing.T) {
	html, err := server.RenderPortsFragment([]server.PortVM{{
		Port: 40123, Label: "x", Cmd: `<script>alert(1)</script>`, Cwd: "/tmp", Uptime: "1s", Source: "ext",
	}})
	require.NoError(t, err)
	require.NotContains(t, html, "<script>alert(1)</script>")
	require.Contains(t, html, "&lt;script&gt;alert(1)&lt;/script&gt;")
}

func TestHTTPServerTimeoutsMatchIronLaw(t *testing.T) {
	cfg := mustCfg(t)
	srv := server.New(cfg)
	hs := srv.NewHTTPServer("127.0.0.1:0")
	require.Equal(t, 5*time.Second, hs.ReadTimeout)
	require.Equal(t, 10*time.Second, hs.WriteTimeout)
	require.Equal(t, 60*time.Second, hs.IdleTimeout)
	require.Equal(t, 2*time.Second, hs.ReadHeaderTimeout)
}

func TestSanitiseNextRejectsExternalRedirect(t *testing.T) {
	ts, _ := newTS(t)
	c := nonRedirectClient(t, ts)
	form := url.Values{
		"token": {testToken},
		"next":  {"https://attacker.example/owned"},
	}
	resp, err := c.PostForm(ts.URL+"/login", form)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	loc := resp.Header.Get("Location")
	require.True(t, strings.HasPrefix(loc, "/"), "expected relative redirect, got %q", loc)
	require.False(t, strings.HasPrefix(loc, "//"), "no protocol-relative URL allowed")
}

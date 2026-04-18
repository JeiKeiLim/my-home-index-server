package server

import (
	"bytes"
	"fmt"
	"io"
	"net/http"

	"github.com/JeiKeiLim/my-home-index-server/internal/config"
	"github.com/JeiKeiLim/my-home-index-server/internal/model"
)

// PortVM is the per-row view-model the /ports template fragment
// consumes. The shape lives in internal/model so the handler, the
// security tests, and the JSON encoder all see the same struct; the
// alias here keeps `server.PortVM` available for callers (security
// tests reference it by that name).
type PortVM = model.PortVM

// RenderPortsFragment renders the production /ports fragment from
// `_ports.html` — the same template the live handler executes. Tests
// rely on this helper to assert XSS escaping and to keep the desktop+
// mobile output structurally stable.
func RenderPortsFragment(ports []PortVM) (string, error) {
	var buf bytes.Buffer
	if err := templates.ExecuteTemplate(&buf, "_ports.html", ports); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// PublicURL formats the dashboard-shareable URL for a port. The host
// always comes from cfg.PublicHost — never from the request's Host
// header — so a copy-url click yields a link that works from any
// network the user might be on (anti-scenario A14).
func PublicURL(cfg *config.Config, port int) string {
	if cfg == nil {
		return ""
	}
	return fmt.Sprintf("http://%s:%d/", cfg.PublicHost, port)
}

// NewBearerRequest builds an Authorization-bearing request. Used by
// the security tests to assert auth + CSRF behaviours without spinning
// up a full HTTP client.
func NewBearerRequest(method, url, token string) (*http.Request, error) {
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return req, nil
}

// NewCookieRequest builds a session-cookie-bearing request. The cookie
// value is whatever auth.IssueCookie returned for the test's clock.
func NewCookieRequest(method, url, cookieValue string) (*http.Request, error) {
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Cookie", "pm_session="+cookieValue)
	return req, nil
}

// CaptureLogs redirects the package logger to an in-memory buffer for
// the duration of fn and returns whatever was logged. Used by A20 to
// assert that the AUTH_TOKEN never reaches stderr.
//
// The redirect is process-global (the slog handler captures a single
// sink proxy). Tests that call CaptureLogs concurrently must accept
// interleaved output — but the security suite never does.
func CaptureLogs(fn func()) string {
	var buf bytes.Buffer
	prev := swapLogSink(&buf)
	defer swapLogSink(prev)
	fn()
	return buf.String()
}

// swapLogSink is the test-only entry point for hijacking stderr. The
// production code never calls it; CaptureLogs uses it via the public
// helper above.
func swapLogSink(w io.Writer) io.Writer {
	logSinkMu.Lock()
	defer logSinkMu.Unlock()
	prev := logSink
	logSink = w
	return prev
}

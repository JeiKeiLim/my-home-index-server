package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// requestIDKey is the context key under which a request's trace id is
// stashed by requestIDMiddleware. Use ctx.Value(requestIDKey) — never
// a string literal — so static analysers can spot key collisions.
type ctxKey int

const (
	requestIDKey ctxKey = iota
	authKindKey
)

// AuthKindBearer / AuthKindCookie label the credential type a request
// authenticated with. The CSRF middleware uses this label to skip the
// X-Requested-With check for bearer-auth callers (per spec §4
// "Bearer-auth callers are CSRF-exempt").
const (
	AuthKindNone   = ""
	AuthKindBearer = "bearer"
	AuthKindCookie = "cookie"
	AuthKindBypass = "bypass" // used by tests with no Auth wired
)

// logSinkMu guards swaps of logSink during CaptureLogs. Using a mutex
// instead of an atomic.Value lets the writer be any io.Writer (which
// atomic.Value rejects unless wrapped) and keeps the test path
// straightforward — production swaps logSink at most once.
var (
	logSinkMu sync.RWMutex
	logSink   io.Writer = os.Stderr
)

// currentLogSink returns the writer the package logger should write to.
// Wrapped in a sinkProxy so the slog.Handler captures the proxy once and
// the pointer dereference happens per-write — that way CaptureLogs can
// rebind logSink without rebuilding handlers.
func currentLogSink() io.Writer {
	logSinkMu.RLock()
	defer logSinkMu.RUnlock()
	return logSink
}

type sinkProxy struct{}

func (sinkProxy) Write(p []byte) (int, error) {
	return currentLogSink().Write(p)
}

var pkgLogger = slog.New(slog.NewJSONHandler(sinkProxy{}, &slog.HandlerOptions{
	Level: slog.LevelInfo,
}))

// requestIDMiddleware tags every incoming request with an 8-byte hex
// trace id, propagated via context for downstream logs and as the
// X-Request-Id response header for client-side correlation.
func requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := newRequestID()
		ctx := context.WithValue(r.Context(), requestIDKey, id)
		w.Header().Set("X-Request-Id", id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func newRequestID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Fallback to nanos if crypto/rand fails — vanishingly rare,
		// and the trace id is informational, not load-bearing.
		return fmt.Sprintf("%016x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// statusRecorder snoops the status code so the logger can record it
// after the handler returns. http.ResponseWriter has no public getter
// for the status; wrapping is the conventional workaround.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	n, err := s.ResponseWriter.Write(b)
	s.bytes += n
	return n, err
}

// loggerMiddleware emits one structured log line per request. The
// header set is redacted before serialising so AUTH_TOKEN and the
// session cookie value cannot leak into stderr (Iron Law #4 / A20).
func loggerMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}

		next.ServeHTTP(rec, r)

		id, _ := r.Context().Value(requestIDKey).(string)
		pkgLogger.Info("http",
			slog.String("request_id", id),
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.String("remote", remoteAddr(r)),
			slog.Int("status", rec.status),
			slog.Int("bytes", rec.bytes),
			slog.Duration("dur", time.Since(start)),
			slog.String("ua", r.UserAgent()),
			slog.String("auth_header", redactAuthHeader(r.Header.Get("Authorization"))),
			slog.String("cookie", redactCookieHeader(r.Header.Get("Cookie"))),
		)
	})
}

// redactAuthHeader keeps the scheme but elides the credential. Empty
// input returns empty so missing auth doesn't get a misleading "[REDACTED]".
func redactAuthHeader(v string) string {
	if v == "" {
		return ""
	}
	scheme := v
	if i := strings.IndexByte(v, ' '); i > 0 {
		scheme = v[:i]
	}
	return scheme + " [REDACTED]"
}

// redactCookieHeader strips the value from any pm_session cookie in
// the Cookie header while preserving every other cookie verbatim. We
// do not blanket-redact: most cookies (e.g. browser telemetry) are
// fine to log and useful for debugging, but pm_session is a bearer-
// equivalent credential.
//
// The segment's original leading whitespace is preserved so the
// reassembled header is byte-for-byte identical to the input except
// for the redacted value — this makes golden-log comparisons in tests
// stable regardless of which position pm_session occupies.
func redactCookieHeader(v string) string {
	if v == "" {
		return ""
	}
	parts := strings.Split(v, ";")
	for i, part := range parts {
		kv := strings.TrimLeft(part, " ")
		leading := part[:len(part)-len(kv)]
		if eq := strings.Index(kv, "="); eq > 0 && strings.EqualFold(kv[:eq], "pm_session") {
			parts[i] = leading + kv[:eq+1] + "[REDACTED]"
		}
	}
	return strings.Join(parts, ";")
}

// recoverMiddleware turns a handler panic into a 500 response and a
// logged stack frame. Without this, a panic inside an http.Handler
// crashes the goroutine, which is benign for net/http (other requests
// continue) but loses the trace id and yields a useless EOF on the
// wire. We log the stringified panic only; production already redacts
// secrets, and the failed-request log is cheaper than restarting the
// whole binary.
func recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				id, _ := r.Context().Value(requestIDKey).(string)
				pkgLogger.Error("panic",
					slog.String("request_id", id),
					slog.Any("panic", rec),
				)
				http.Error(w, "internal server error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// requireAuth wraps next with authentication. Bearer-auth wins if
// present; otherwise we look for a valid pm_session cookie. On
// failure, mutation routes return 401 (machine-readable) while a HTML
// GET would return 401 too — only the dashboard root uses
// requireAuthOrRedirect for the 303-to-login behaviour.
func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if kind, ok := s.authenticate(r); ok {
			ctx := context.WithValue(r.Context(), authKindKey, kind)
			next(w, r.WithContext(ctx))
			return
		}
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}
}

// requireAuthOrRedirect is the GET / variant: unauthed requests get a
// 303 redirect to /login. Anything authed proceeds to next. The sole
// caller is `GET /{$}`, so the redirect target is always just /login
// — there is no `?next=…` branch because the route pattern only
// matches "/". When job-8+ adds more HTML-authed routes, broaden this
// to preserve the original path then.
func (s *Server) requireAuthOrRedirect(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if kind, ok := s.authenticate(r); ok {
			ctx := context.WithValue(r.Context(), authKindKey, kind)
			next(w, r.WithContext(ctx))
			return
		}
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	}
}

// authenticate inspects Authorization and pm_session in turn. It
// returns the kind that succeeded or "" with ok=false. With s.auth nil
// (test stubs constructed via `New(cfg, nil, nil…)`), authentication
// always fails so unauthed callers get 401 — the right default.
func (s *Server) authenticate(r *http.Request) (string, bool) {
	if s.auth == nil {
		return "", false
	}
	if authz := r.Header.Get("Authorization"); strings.HasPrefix(authz, "Bearer ") {
		token := strings.TrimSpace(authz[len("Bearer "):])
		if s.auth.CheckBearer(token) {
			return AuthKindBearer, true
		}
		return "", false
	}
	if c, err := r.Cookie("pm_session"); err == nil && c.Value != "" {
		if s.auth.VerifyCookie(c.Value) {
			return AuthKindCookie, true
		}
	}
	return "", false
}

// withRateLimit applies the per-IP sliding-window rate limit to the
// wrapped handler. Wired only on POST /login per spec §4 — applying
// it elsewhere would block htmx polling traffic from a single client.
func (s *Server) withRateLimit(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.auth == nil {
			next(w, r)
			return
		}
		ip := remoteIP(r, s.trustXFF())
		allowed, retry := s.auth.RateLimitCheck(ip)
		if !allowed {
			seconds := int(retry.Seconds())
			if seconds < 1 {
				seconds = 1
			}
			w.Header().Set("Retry-After", fmt.Sprintf("%d", seconds))
			http.Error(w, "too many login attempts; try again later", http.StatusTooManyRequests)
			return
		}
		next(w, r)
	}
}

// withCSRF rejects mutation requests that came in on a session cookie
// without the X-Requested-With header that htmx sets automatically.
// Bearer-auth callers (CLIs, scripts) are exempt per spec §4.
func (s *Server) withCSRF(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		kind, _ := r.Context().Value(authKindKey).(string)
		if kind == AuthKindBearer {
			next(w, r)
			return
		}
		if r.Header.Get("X-Requested-With") != "XMLHttpRequest" {
			http.Error(w, "csrf: missing X-Requested-With", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

// remoteAddr returns the IP portion of r.RemoteAddr, falling back to
// the raw string if it cannot be split. Used as the log field for the
// raw transport peer regardless of trust mode.
func remoteAddr(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// remoteIP returns the rate-limit key for r. When trustXFF is true,
// the first non-empty segment of X-Forwarded-For is used (with quote
// and whitespace stripped) — this is the original-client identity a
// trusted reverse proxy injects. With trustXFF false, or when the
// header is absent / contains only empty segments, we fall back to
// the transport-level peer address from r.RemoteAddr.
//
// SECURITY: trustXFF MUST only be enabled behind a reverse proxy that
// always rewrites X-Forwarded-For. On a naked HTTP listener an
// attacker can spoof the header to bypass per-IP rate-limiting by
// rotating through a fresh bucket key per request.
func remoteIP(r *http.Request, trustXFF bool) string {
	if trustXFF {
		if hop := firstXFFHop(r.Header.Get("X-Forwarded-For")); hop != "" {
			return hop
		}
	}
	return remoteAddr(r)
}

// firstXFFHop returns the first non-empty, trimmed entry in an
// X-Forwarded-For header. Surrounding double-quotes are stripped so a
// proxy that wraps IPv6 literals like `"[::1]"` still produces a
// stable bucket key.
func firstXFFHop(v string) string {
	if v == "" {
		return ""
	}
	for _, part := range strings.Split(v, ",") {
		hop := strings.TrimSpace(part)
		hop = strings.Trim(hop, "\"")
		hop = strings.TrimSpace(hop)
		if hop != "" {
			return hop
		}
	}
	return ""
}

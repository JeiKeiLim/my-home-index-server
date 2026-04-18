package server

import (
	"io/fs"
	"net/http"
	"strings"

	"github.com/JeiKeiLim/my-home-index-server/web"
)

// buildHandler constructs the ServeMux and threads the middleware
// pipeline around it. The pipeline order — RequestID → Logger →
// Recover → Auth → RateLimit (login only) → CSRF (mutation only) — is
// fixed by spec §4 and the Iron Laws: RequestID must precede Logger
// so every log line has a trace id, Recover must precede Auth so a
// panic inside Auth itself doesn't crash the process, and CSRF runs
// last so it sees the effective auth subject.
//
// Per-route middleware (RateLimit on POST /login; CSRF on mutation
// routes) is layered inside the per-route handler since http.ServeMux
// does not expose a per-route hook.
func (s *Server) buildHandler() http.Handler {
	mux := http.NewServeMux()

	// Public routes: no auth, no CSRF. RateLimit attaches inline on
	// POST /login because middleware ordering is fixed (RateLimit only
	// applies to that route, not site-wide).
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /login", s.handleLoginGet)
	mux.HandleFunc("POST /login", s.withRateLimit(s.handleLoginPost))

	// Static assets are public too — no secrets reside there. A
	// trailing-slash request falls through to 404: http.FileServer
	// defaults to rendering a directory index, which would enumerate
	// every embedded asset (a small information leak and not needed for
	// our SPA shell).
	staticSub, err := fs.Sub(web.Static, "static")
	if err != nil {
		panic("server: sub static fs: " + err.Error())
	}
	staticHandler := http.StripPrefix("/static/", http.FileServer(http.FS(staticSub)))
	mux.Handle("GET /static/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/") {
			http.NotFound(w, r)
			return
		}
		staticHandler.ServeHTTP(w, r)
	}))

	// Authenticated routes. The auth middleware short-circuits with
	// 303→/login for HTML clients and 401 for API/mutation clients.
	// POST /logout is a state-changing mutation (session teardown), so
	// it gets the same withCSRF guard as /kill/{port} etc. Bearer-auth
	// callers are CSRF-exempt by withCSRF's own logic, so `curl -X POST
	// -H "Authorization: Bearer …"` still works for scripted sign-out.
	mux.HandleFunc("POST /logout", s.requireAuth(s.withCSRF(s.handleLogout)))
	mux.HandleFunc("GET /{$}", s.requireAuthOrRedirect(s.handleDashboard))

	// Mutation route placeholders — wired here so the auth + CSRF
	// middleware runs even before jobs 8/9 land the real handlers.
	// Returning 501 keeps the contract honest: "auth + CSRF passed,
	// but the action isn't built yet".
	mux.HandleFunc("POST /kill/{port}", s.requireAuth(s.withCSRF(s.handleKill)))
	mux.HandleFunc("POST /restart/{id}", s.requireAuth(s.withCSRF(s.handleRestart)))
	mux.HandleFunc("POST /rename/{port}", s.requireAuth(s.withCSRF(s.handleRename)))

	// Read-only data routes (filled by job-8). Auth is required so
	// secrets in error paths cannot be probed by unauthed clients.
	mux.HandleFunc("GET /ports", s.requireAuth(s.handlePortsHTML))
	mux.HandleFunc("GET /ports.json", s.requireAuth(s.handlePortsJSON))
	// /remembered renders the dashboard's "restart history" view —
	// same fragment shape as /ports but built from the store's
	// remembered entries instead of live scanner output.
	mux.HandleFunc("GET /remembered", s.requireAuth(s.handleRememberedList))

	// Wrap mux in the site-wide pipeline. Order matters: outermost
	// runs first on the way in, last on the way out.
	var h http.Handler = mux
	h = recoverMiddleware(h)
	h = loggerMiddleware(h)
	h = requestIDMiddleware(h)
	return h
}

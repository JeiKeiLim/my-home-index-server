package server

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// loginView is the data passed to login.html. Keeping it as a flat
// struct (no map) means the template can rely on field types and the
// compiler catches typos in the template at render time.
type loginView struct {
	PublicHost string
	Next       string
	Error      string
}

// dashboardView is the data passed to dashboard.html.
type dashboardView struct {
	PublicHost string
	PortMin    int
	PortMax    int
	User       string
}

// handleHealthz is the tiny liveness probe. Plain text on purpose:
// uptime checks parse "ok" with grep, not a JSON parser.
func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok"))
}

// handleLoginGet renders the login form. Passes through the ?next=
// query so a successful POST can bounce the user back to the page
// they were trying to reach.
func (s *Server) handleLoginGet(w http.ResponseWriter, r *http.Request) {
	if s.cookieAuthed(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	view := loginView{
		PublicHost: s.publicHost(),
		Next:       sanitiseNext(r.URL.Query().Get("next")),
	}
	body, err := renderTemplate("login.html", view)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(body)
}

// handleLoginPost processes the form submission. Constant-time token
// compare on success → cookie + 303. On failure → record + re-render
// with a generic message (no oracle).
func (s *Server) handleLoginPost(w http.ResponseWriter, r *http.Request) {
	if s.auth == nil {
		http.Error(w, "auth not configured", http.StatusServiceUnavailable)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	token := r.PostForm.Get("token")
	next := sanitiseNext(r.PostForm.Get("next"))
	ip := remoteIP(r, s.trustXFF())

	if !s.auth.CheckBearer(token) {
		s.auth.RateLimitRecordFailure(ip)
		view := loginView{
			PublicHost: s.publicHost(),
			Next:       next,
			Error:      "invalid token",
		}
		body, _ := renderTemplate("login.html", view)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write(body)
		return
	}

	s.auth.RateLimitRecordSuccess(ip)
	cookie := s.auth.IssueCookie(time.Now())
	http.SetCookie(w, &http.Cookie{
		Name:     "pm_session",
		Value:    cookie,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		// Secure intentionally false — spec §7 ships plain HTTP MVP;
		// production users front with Caddy/nginx for TLS.
		Expires: time.Now().Add(24 * time.Hour),
	})
	dest := next
	if dest == "" {
		dest = "/"
	}
	http.Redirect(w, r, dest, http.StatusSeeOther)
}

// handleLogout clears the cookie and bounces to /login. For htmx
// callers the redirect is surfaced via the HX-Redirect header so the
// browser does a full navigation instead of swapping the login page
// HTML into the dashboard DOM. The 303 Location is kept so non-htmx
// clients (curl, server_test) still see the expected redirect.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     "pm_session",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
	if r.Header.Get("X-Requested-With") == "XMLHttpRequest" {
		w.Header().Set("HX-Redirect", "/login")
	}
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// handleDashboard renders the dashboard shell — empty tbody/cards
// blocks that htmx fills via /ports polling. Job-8 wires /ports.
func (s *Server) handleDashboard(w http.ResponseWriter, _ *http.Request) {
	view := dashboardView{
		PublicHost: s.publicHost(),
		PortMin:    s.portMin(),
		PortMax:    s.portMax(),
		User:       s.user,
	}
	body, err := renderTemplate("dashboard.html", view)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(body)
}

// handleKill dispatches POST /kill/{port}. Job-9 wired the real flow
// in mutation_handlers.go — this shim keeps the router stable and
// short-circuits on a malformed or out-of-range port before touching
// the process manager, satisfying TestA8_PortRangeGuard even when the
// server is constructed without a process manager.
func (s *Server) handleKill(w http.ResponseWriter, r *http.Request) {
	port, err := strconv.Atoi(r.PathValue("port"))
	if err != nil {
		http.Error(w, "bad port", http.StatusBadRequest)
		return
	}
	if !s.portInRange(port) {
		http.Error(w, "port out of range", http.StatusBadRequest)
		return
	}
	s.handleKillMutation(w, r)
}

// handleRestart dispatches POST /restart/{id} to the job-9 impl.
func (s *Server) handleRestart(w http.ResponseWriter, r *http.Request) {
	s.handleRestartMutation(w, r)
}

// handleRename dispatches POST /rename/{port} to the job-9 impl after
// the port range guard. Route validation stays at the router edge so
// range errors surface as 400 regardless of store wiring.
func (s *Server) handleRename(w http.ResponseWriter, r *http.Request) {
	port, err := strconv.Atoi(r.PathValue("port"))
	if err != nil {
		http.Error(w, "bad port", http.StatusBadRequest)
		return
	}
	if !s.portInRange(port) {
		http.Error(w, "port out of range", http.StatusBadRequest)
		return
	}
	s.handleRenameMutation(w, r)
}

// cookieAuthed reports whether the request already carries a valid
// session cookie. Used by handleLoginGet to skip the form for users
// who navigated to /login while already logged in.
func (s *Server) cookieAuthed(r *http.Request) bool {
	if s.auth == nil {
		return false
	}
	c, err := r.Cookie("pm_session")
	if err != nil || c.Value == "" {
		return false
	}
	return s.auth.VerifyCookie(c.Value)
}

func (s *Server) publicHost() string {
	if s.cfg == nil {
		return ""
	}
	return s.cfg.PublicHost
}

func (s *Server) portMin() int {
	if s.cfg == nil {
		return 0
	}
	return s.cfg.PortMin
}

func (s *Server) portMax() int {
	if s.cfg == nil {
		return 0
	}
	return s.cfg.PortMax
}

func (s *Server) portInRange(p int) bool {
	if s.cfg == nil {
		// With no config, deny everything — refusing to act is the
		// safer default than letting the test stub kill arbitrary pids.
		return false
	}
	return p >= s.cfg.PortMin && p <= s.cfg.PortMax
}

// sanitiseNext keeps only relative URLs whose path begins with /, so a
// crafted ?next=https://attacker.example/foo cannot turn the post-login
// redirect into an open redirect.
func sanitiseNext(v string) string {
	if v == "" {
		return ""
	}
	if !strings.HasPrefix(v, "/") || strings.HasPrefix(v, "//") {
		return ""
	}
	return v
}

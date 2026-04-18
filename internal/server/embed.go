// Package server is the HTTP shell for port-manager. The shell wires
// templates, middleware (RequestID → Logger → Recover → Auth →
// RateLimit on /login → CSRF on mutations), and the handler set
// (/healthz, /login, /logout, GET /, /static/) defined in spec §4.
//
// Templates and static assets live under web/ at the repo root and are
// embedded into the binary by the web package, then pre-parsed at
// startup (template parse errors panic rather than surface at request
// time, per Iron Law #11).
package server

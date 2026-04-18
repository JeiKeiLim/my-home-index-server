package server

import (
	"context"
	"net/http"
	"os"
	"os/user"
	"time"

	"github.com/JeiKeiLim/my-home-index-server/internal/auth"
	"github.com/JeiKeiLim/my-home-index-server/internal/config"
	"github.com/JeiKeiLim/my-home-index-server/internal/inspector"
	"github.com/JeiKeiLim/my-home-index-server/internal/process"
	"github.com/JeiKeiLim/my-home-index-server/internal/scanner"
	"github.com/JeiKeiLim/my-home-index-server/internal/store"
)

// ProcessManager is the subset of *process.Manager the mutation
// handlers depend on. Declared here (not in internal/process) so tests
// can substitute fakes without touching the public API of the
// production process package.
type ProcessManager interface {
	Kill(ctx context.Context, t process.Target) error
	Restart(ctx context.Context, id string) error
}

// Server bundles the dependencies the HTTP handlers need plus the
// pre-built ServeMux + middleware chain. New always returns a usable
// *Server even when called with nil sub-deps — handlers that need a
// missing dep return 503 rather than panicking, which keeps the
// acceptance-test stub usage (`server.New(cfg, nil, nil, nil, nil)`)
// alive while job-8 / job-9 land the rest.
type Server struct {
	cfg       *config.Config
	auth      *auth.Auth
	scanner   scanner.Scanner
	inspector inspector.Inspector
	process   ProcessManager
	store     *store.Store
	user      string
	selfPID   int
	snapshot  *snapshotCache
	handler   http.Handler
}

// HTTP server timeouts mandated by Iron Law #12.
const (
	ReadTimeout       = 5 * time.Second
	WriteTimeout      = 10 * time.Second
	IdleTimeout       = 60 * time.Second
	ReadHeaderTimeout = 2 * time.Second
)

// New constructs a Server. cfg is required; the variadic deps argument
// accepts (in any order) typed components so the call sites in main.go
// and in the acceptance tests can both pass them. Nil deps are tolerated
// so the security tests can construct a Server with `nil, nil, nil, nil`.
//
// If no *auth.Auth is supplied, one is built from cfg here — keeping
// the rate-limit + cookie machinery local to this Server instance.
func New(cfg *config.Config, deps ...any) *Server {
	s := &Server{cfg: cfg, selfPID: os.Getpid(), snapshot: &snapshotCache{}}
	for _, d := range deps {
		if d == nil {
			continue
		}
		switch v := d.(type) {
		case *auth.Auth:
			s.auth = v
		case scanner.Scanner:
			s.scanner = v
		case inspector.Inspector:
			s.inspector = v
		case *process.Manager:
			s.process = v
		case ProcessManager:
			s.process = v
		case *store.Store:
			s.store = v
		}
	}
	if s.auth == nil && cfg != nil {
		s.auth = auth.New(cfg)
	}
	if u, err := user.Current(); err == nil {
		s.user = u.Username
	}
	s.handler = s.buildHandler()
	return s
}

// Handler returns the fully-middlewared http.Handler.
func (s *Server) Handler() http.Handler { return s.handler }

// trustXFF reports whether the rate-limit + login handlers should
// derive the remote-IP key from X-Forwarded-For. Returns false when
// cfg is nil so the test-stub Server (built with no config) keeps the
// safe default and never picks XFF-controlled buckets.
func (s *Server) trustXFF() bool {
	if s.cfg == nil {
		return false
	}
	return s.cfg.TrustXFF
}

// NewHTTPServer wraps the Server's handler in a stdlib *http.Server
// configured with the timeouts dictated by Iron Law #12. Callers in
// cmd/port-manager use this to obtain a ready-to-Listen instance.
func (s *Server) NewHTTPServer(addr string) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           s.handler,
		ReadTimeout:       ReadTimeout,
		WriteTimeout:      WriteTimeout,
		IdleTimeout:       IdleTimeout,
		ReadHeaderTimeout: ReadHeaderTimeout,
	}
}

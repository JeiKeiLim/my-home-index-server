# Decomposition — port-manager

Date: 2026-04-17 · Feature: `port-manager`
Spec: `.tenet/spec/2026-04-17-port-manager.md`
Scenarios: `.tenet/spec/scenarios-2026-04-17-port-manager.md`
Design: `.tenet/DESIGN.md` · Mockup: `.tenet/visuals/2026-04-17-01-mockup-terminal-dark.html`
Research: `.tenet/knowledge/2026-04-17_research-macos-tcp-listener-enumeration.md`,
`.tenet/knowledge/2026-04-17_research-macos-process-metadata.md`,
`.tenet/knowledge/2026-04-17_research-macos-external-process-log-capture.md`

## DAG (ASCII)

```
                              ┌────────────────────┐
                              │ job-1  scaffold    │
                              └────────┬───────────┘
                                       │
         ┌─────────────┬────────────┬──┴──────────┬─────────────┐
         │             │            │             │             │
   ┌─────▼─────┐ ┌─────▼─────┐ ┌────▼────┐ ┌──────▼────┐  (acceptance test
   │ job-2     │ │ job-3     │ │ job-4   │ │ job-5     │   stubs are
   │ config    │ │ scanner   │ │ inspect │ │ store     │   already written
   │ + auth    │ │ cgo+lsof  │ │ gopsutil│ │ labels+   │   under tests/
   └─────┬─────┘ └─────┬─────┘ └────┬────┘ │ remembered│   acceptance/)
         │             │            │      └──────┬────┘
         └─────────────┴─────┬──────┴─────────────┘
                             │
                  ┌──────────▼───────────┐
                  │ e2e-1  [integration] │
                  │ scan+inspect+store   │
                  └──────────┬───────────┘
                             │
                   ┌─────────┴──────────┐
                   │                    │
          ┌────────▼───────┐   ┌────────▼──────────┐
          │ job-6  process │   │ job-7 server-shell│
          │ kill/restart/  │   │ router/middleware/│
          │ reaper         │   │ login/logout/    │
          └────────┬───────┘   │ healthz/templates│
                   │           └────────┬──────────┘
                   │                    │
                   │          ┌─────────▼──────────┐
                   │          │ job-8 dashboard-ui │
                   │          │ GET /, /ports,     │
                   │          │ /ports.json,       │
                   │          │ table+cards+       │
                   │          │ modals+toasts      │
                   │          └─────────┬──────────┘
                   │                    │
                   └────────┬───────────┘
                            │
                  ┌─────────▼─────────┐
                  │ job-9 mutation-   │
                  │ routes: /kill,    │
                  │ /restart, /rename │
                  │ + CSRF            │
                  └─────────┬─────────┘
                            │
                  ┌─────────▼─────────┐
                  │ job-10 polish:    │
                  │ README, release   │
                  │ build, CI,        │
                  │ smoke script      │
                  └─────────┬─────────┘
                            │
                  ┌─────────▼────────────┐
                  │ e2e-final            │
                  │ [integration]        │
                  │ all Playwright +     │
                  │ anti-scenario tests  │
                  └──────────────────────┘
```

10 dev jobs + 2 integration checkpoints = 12 total jobs.

## Interface contracts (boundaries between jobs)

### config (job-2)
```go
type Config struct {
    AuthToken     string
    SessionSecret string
    PublicHost    string
    Port          int       // dashboard listen port
    PortMin       int       // 40000
    PortMax       int       // 40500
    KillGraceMS   int       // 3000
    Scanner       string    // "auto" | "libproc" | "lsof"
    StateDir      string    // default ~/.port-manager
}
func Load(opts Options) (*Config, error)
```

### auth (job-2)
```go
type Auth struct{ ... }
func New(cfg *Config) *Auth
func (a *Auth) CheckBearer(token string) bool
func (a *Auth) IssueCookie(issued time.Time) string
func (a *Auth) VerifyCookie(value string) bool
func (a *Auth) RateLimitCheck(ip string) (allowed bool, retryAfter time.Duration)
func (a *Auth) RateLimitRecordFailure(ip string)
func (a *Auth) RateLimitRecordSuccess(ip string)
```

### scanner (job-3)
```go
type Listener struct {
    PID      int
    Port     int
    Protocol string       // "tcp"
    Addrs    []string     // e.g. ["0.0.0.0:40123", "[::1]:40123"]
    Source   string       // "libproc" | "lsof"
}
type Scanner interface {
    Scan(ctx context.Context) ([]Listener, error)
    Name() string
}
func Auto(cfg *Config) (Scanner, error)
func NewLibproc(cfg *Config) (Scanner, error) // darwin only
func NewLsof(cfg *Config) (Scanner, error)
```

### inspector (job-4)
```go
type ProcInfo struct {
    PID       int
    Command   []string     // argv
    Cwd       string       // absolute
    StartTime time.Time
    UID       int
    Env       []string     // KEY=VAL entries (best-effort)
}
type Inspector interface {
    Inspect(ctx context.Context, pid int) (*ProcInfo, error)
}
func NewGopsutil() Inspector
```

### store (job-5)
```go
type Remembered struct {
    ID        string
    Port      int
    Command   []string
    Cwd       string
    Env       []string
    KilledAt  time.Time
    LabelKey  string
}
type Store struct{ ... }
func Open(homeDir string) (*Store, error)
func (s *Store) Label(cwd, cmd string) (string, error)
func (s *Store) SetLabel(cwd, cmd, label string) error
func (s *Store) Remember(r Remembered) error
func (s *Store) ListRemembered(cwd, cmd string) ([]Remembered, error)
func (s *Store) FindRemembered(id string) (*Remembered, error)
func (s *Store) DeleteRemembered(id string) error
```

### process (job-6)
```go
type Target struct { PID, Port int }
type Spec struct {
    Command []string
    Cwd     string
    Env     []string
}
var ErrSelfPID = errors.New("refusing to act on self PID")
type Manager struct { ... }
func New(cfg *Config) (*Manager, error)
func (m *Manager) Kill(ctx context.Context, t Target) error    // SIGTERM → grace → SIGKILL
func (m *Manager) Restart(ctx context.Context, id string) error
func (m *Manager) SpawnDetached(ctx context.Context, s Spec) error  // used by Restart internally
func (m *Manager) SpawnForTest(ctx context.Context, s Spec) (pid int, err error)
```

### server (job-7, extended in job-8, job-9)
```go
type Deps struct {
    Cfg       *Config
    Auth      *Auth
    Scanner   Scanner
    Inspector Inspector
    Process   *Manager
    Store     *Store
}
type Server struct { ... }
func New(deps Deps) *Server
func (s *Server) Handler() http.Handler
// template rendering helpers exposed for tests
func RenderPortsFragment(ports []PortVM) (string, error)
func PublicURL(cfg *Config, port int) string
// request helpers for integration tests
func NewBearerRequest(method, url, token string) (*http.Request, error)
func NewCookieRequest(method, url, cookieValue string) (*http.Request, error)
func CaptureLogs(fn func()) string
```

---

## Jobs

### job-1 · scaffold
- **Type:** dev · **Deps:** none
- **Deliverables:**
  - `go.mod` with module path `github.com/JeiKeiLim/my-home-index-server`, Go 1.23+
  - Directory skeleton: `cmd/port-manager/`, `internal/{config,auth,scanner,inspector,process,store,server,model}/`, `web/{static,templates}/`, `tests/{integration,acceptance}/`
  - `.env.example` with all variables documented
  - `.gitignore` (ignores `.env`, `port-manager` binary, `state.json`, `node_modules`, `playwright-report`, `.seed-pids.json`)
  - `Makefile` with targets: `build`, `test`, `lint`, `smoke`, `e2e`, `release`
  - `cmd/port-manager/main.go` that compiles but just prints `"port-manager scaffold ok"` and exits
  - `tools/tools.go` pinning `golangci-lint` version
  - Minimal `README.md` pointing at the spec
  - `package.json` + `playwright.config.ts` symlink/ref from `tests/acceptance/e2e/` for running e2e
- **Verification:**
  - `go build ./...` succeeds
  - `go vet ./...` clean
  - `./port-manager` prints the scaffold banner

### job-2 · config + auth
- **Type:** dev · **Deps:** [job-1]
- **Deliverables:**
  - `internal/config/config.go`: loads `.env` via godotenv, merges CLI flags, validates (ports, grace ms, token/secret lengths, auto-generates + persists AUTH_TOKEN and SESSION_SECRET to `.env` if empty).
  - `internal/auth/auth.go`: constant-time bearer compare, HMAC-signed cookie (issuedAt||hmac), verify w/ 24h TTL, in-process rate limiter (per-IP sliding window 15min, capacity 5, lockout 15min).
  - Unit tests: `internal/config/config_test.go`, `internal/auth/auth_test.go`.
- **Verification:**
  - `go test -race ./internal/config ./internal/auth` passes
  - Acceptance helpers `TestA6`, `TestA7` (stubs in `tests/acceptance/security/`) compile (note: they don't need to pass yet — they depend on `process` & `server` wiring too)
  - Coverage ≥ 80% in both packages

### job-3 · scanner
- **Type:** dev · **Deps:** [job-1]
- **Deliverables:**
  - `internal/scanner/scanner.go`: interface, `Auto(cfg)` selects impl
  - `internal/scanner/libproc_darwin.go` (build tag `//go:build darwin`): cgo walk `proc_listallpids` → `proc_pidinfo(PROC_PIDTBSDINFO)` uid filter → `proc_pidinfo(PROC_PIDLISTFDS)` → `proc_pidfdinfo(PROC_PIDFDSOCKETINFO)` for TCP LISTEN in range.
  - `internal/scanner/lsof.go`: `exec.CommandContext` with 1s timeout, `-iTCP -sTCP:LISTEN -P -n -a -u $(id -u) -F pPn -b`, `cmd.Output()`, stderr discarded.
  - Integration test spawns a real Python listener on a port in test sub-range and asserts both scanners find it.
- **Verification:**
  - `go test -race ./internal/scanner` passes
  - Scanner excludes own PID (`os.Getpid()`) in unit test
  - Scanner excludes ports outside config range
  - `TestA9_ScannerIgnoresUDP` compiles & passes
  - `TestA10_ScannerRespectsContextTimeout` passes
  - `TestA11_NoFDLeakOverManyScans` passes (500-iteration fd stability)

### job-4 · inspector
- **Type:** dev · **Deps:** [job-1]
- **Deliverables:**
  - `internal/inspector/inspector.go` wrapping gopsutil v4 for Cmdline/CmdlineSlice/Cwd/CreateTime/Uids
  - Small cgo helper for `KERN_PROCARGS2` to retrieve env (gopsutil Environ not implemented on Darwin) — file `internal/inspector/envp_darwin.go`
  - Returns `ProcInfo` struct; graceful errors for EPERM (cross-user)
- **Verification:**
  - `go test -race ./internal/inspector` passes
  - Integration test: spawn a child with known cwd+args+env, inspect it, assert round-trip

### job-5 · store
- **Type:** dev · **Deps:** [job-1]
- **Deliverables:**
  - `internal/store/store.go`: JSON persistence to `~/.port-manager/state.json`, `sync.Mutex`, atomic write helper `writeAtomic(path, data)` (tmp + fsync + rename).
  - Labels keyed by `sha256hex(cwd \x00 command)[:16]`; remembered list with per-key cap 5 + global cap 100.
  - Hydration at startup: reads state.json, ignores stale `.tmp` files.
- **Verification:**
  - `go test -race -count=5 ./internal/store` passes (including TestA5 — 100 concurrent renames)
  - `TestA15_AtomicWrite` passes
  - `TestA17_RememberedListIsCapped` passes
  - Coverage ≥ 90% (store is pure logic, no I/O to external services)

### e2e-1 · integration checkpoint
- **Type:** integration_test · **Deps:** [job-2, job-3, job-4, job-5]
- **Prompt (to integration agent):** "Run `go test -race -shuffle=on ./internal/config ./internal/auth ./internal/scanner ./internal/inspector ./internal/store ./tests/acceptance/security/...`. Report any failing tests. Do NOT fix code — only report."
- **Success criteria:** All unit tests pass; race detector clean. Anti-scenario stubs that reference only config/auth/scanner/inspector/store compile successfully even if some (A1, A8, A13, A14, A16, A19, A20) still fail (they need `process` and `server`).

### job-6 · process manager
- **Type:** dev · **Deps:** [e2e-1]
- **Deliverables:**
  - `internal/process/process.go`: Kill (SIGTERM → wait grace → SIGKILL), Restart (looks up remembered in store, calls SpawnDetached), SpawnDetached (`os/exec` with `SysProcAttr{Setpgid:true, Setsid:true}`, stdin/stdout/stderr → /dev/null, reaper goroutine).
  - Self-PID guard: `ErrSelfPID` returned if `target.PID == os.Getpid()`; two-layer check (at entry to Kill AND at the scanner boundary used to look up live port-to-PID).
  - Integration tests using the stubborn-child helper.
- **Verification:**
  - `go test -race ./internal/process` passes, including:
    - `TestA1_KillSelfIsRefused`
    - `TestA2_CrossUserIsRefused`
    - `TestA4_RestartUsesStoredCwdOnly` (compile-time)
    - `TestA12_NoZombiesAfterRestart`
    - `TestA16_RestartUsesNewProcessGroup`
  - Graceful-kill timing: SIGTERM-honoring child exits within 500ms; SIGTERM-ignoring child gets SIGKILL at grace ± 200ms.

### job-7 · server shell
- **Type:** dev · **Deps:** [e2e-1]
- **Deliverables:**
  - `internal/server/router.go`: `http.ServeMux` with Go 1.22+ pattern routes; middleware pipeline `RequestID → Logger (redacted) → Recover → Auth → RateLimit (login only) → CSRF (mutation routes)`.
  - `internal/server/handlers.go` (shell only): GET/POST `/login`, POST `/logout`, GET `/healthz`, 404 handler. `GET /` redirects to `/login` if unauthed; if authed, returns the dashboard shell template (tbody/cards empty, filled by htmx).
  - `internal/server/render.go`: template registry, `embed.FS` for `web/templates/`, pre-parse on startup.
  - `web/templates/login.html`, `web/templates/dashboard.html` (shell only — `<tbody hx-get="/ports" hx-trigger="load, every 2s" hx-swap="innerHTML">` placeholder), `web/static/htmx.min.js`, `web/static/style.css` (using DESIGN.md tokens).
  - `cmd/port-manager/main.go` wired: load config → open store → construct scanner + inspector + process → construct server → `http.ListenAndServe`. Print bind address + token (if just generated) on stdout.
  - Helpers: `NewBearerRequest`, `NewCookieRequest`, `CaptureLogs` exported in a `testhelpers.go` file (darwin build-tagged for tests).
- **Verification:**
  - `go test -race ./internal/server` passes
  - Manual: `./port-manager --port 40091 --public-host localhost` starts; GET /healthz returns ok; GET /login renders form; POST /login with fixed token sets cookie + 303 to /.
  - Acceptance tests passing at this point: S1 first two, S7 both, S8 rate-limit (via `tests/acceptance/e2e/specs/login.spec.ts`), A6, A7, A20.

### job-8 · dashboard UI
- **Type:** dev · **Deps:** [job-7, e2e-1]
- **Deliverables:**
  - `internal/server/ports_handler.go`: GET `/ports` → HTML fragment (desktop `<tr>` rows + mobile `<div.card>` blocks in one response, using htmx `hx-swap-oob` so both swap from one GET); GET `/ports.json` → JSON array.
  - Template fragment `web/templates/_ports.html` renders both desktop and mobile views from the same `[]PortVM` data.
  - `internal/model/viewmodel.go`: `PortVM` struct flattening scanner + inspector + store (label lookup) data for the template; `BuildViewModels(listeners, inspector, store, selfPID) ([]PortVM, error)`.
  - Data layer composition: `/ports` handler = scanner.Scan (with 1s ctx) → inspector.Inspect each → store.Label lookups → BuildViewModels → template exec.
  - CSS for responsive `@media (max-width: 820px)` swap; modals/toasts from the prototype carried over to real pages.
- **Verification:**
  - Manual visual check against `.tenet/visuals/2026-04-17-01-mockup-terminal-dark.html`
  - Playwright S1 (full — seeded port visible in table)
  - Playwright S6 (mobile viewport shows cards, hides table)
  - Playwright S9 (`/ports` and `/ports.json` responsive under repeated load)
  - `TestA13_XSSInCommandIsEscaped` passes
  - `TestA14_CopyURLUsesPublicHost` passes (requires `PublicURL` exported from server package)

### job-9 · mutation routes
- **Type:** dev · **Deps:** [job-6, job-8]
- **Deliverables:**
  - `POST /kill/{port:int}`: validates port range, resolves PID via fresh scan, calls `process.Kill`, writes to `store.Remember`, returns updated `/ports` fragment (htmx swap) or JSON.
  - `POST /restart/{remembered_id}`: calls `process.Restart`, returns 202 or updated fragment.
  - `POST /rename/{port:int}`: accepts `label=...` form field, validates 0–64 chars, calls `store.SetLabel` with the port's current `(cwd, command)` (resolved via scanner+inspector), returns updated fragment.
  - CSRF guard implemented in middleware (already wired in job-7; extend here to cover these routes explicitly).
  - Wire up mutations in JS-lite within the template: fetch('POST', ...) with `X-Requested-With: XMLHttpRequest`. `copy url` and `copy cwd` use `navigator.clipboard`.
- **Verification:**
  - Playwright S2 (kill → row disappears)
  - Playwright S3 (restart remembered → listener re-appears)
  - Playwright S4 (rename → visible + persists across reload)
  - Playwright S5 (copy URL + copy CWD write expected values to clipboard)
  - `TestA8_PortRangeGuard` passes
  - `TestA19_CSRFGuardRequiresXRW` passes
  - `TestA17_RememberedListIsCapped` verified end-to-end (killing same `(cwd, cmd)` 6 times keeps only 5 remembered).

### job-10 · polish
- **Type:** dev · **Deps:** [job-9]
- **Deliverables:**
  - Full `README.md`: install, run, config, screenshots (reference mockup), security notes (no TLS, plain HTTP), "what this deliberately does NOT do" (no launch, no logs).
  - `.github/workflows/ci.yml` running on `macos-15`: `go vet`, `golangci-lint`, `go test -race -shuffle=on ./...`, `npm ci && npx playwright install --with-deps chromium && npx playwright test` in `tests/acceptance/e2e/`.
  - `Makefile` targets finalized; `make release` produces a trimmed+stripped arm64 binary in `dist/port-manager`.
  - `scripts/smoke.sh` that builds, starts the binary with test AUTH_TOKEN, curls /healthz + /ports.json, kills the binary.
  - Stable build info (version string) in `cmd/port-manager/main.go` via `-ldflags "-X main.version=..."`.
- **Verification:**
  - `make build && make test && make lint && ./scripts/smoke.sh` all green on a fresh clone.
  - CI workflow passes in dry-run.

### e2e-final · integration checkpoint
- **Type:** integration_test · **Deps:** [job-10]
- **Prompt (to integration agent):** "Run the full acceptance suite:
  1. `go test -race -shuffle=on ./...` (all unit + integration + security anti-scenarios)
  2. `cd tests/acceptance/e2e && npm ci && npx playwright install --with-deps chromium && npx playwright test`
  3. `./scripts/smoke.sh`
  Report which tests pass and fail. Do NOT fix code — only report."
- **Success criteria:** Every test in `tests/acceptance/**` and `internal/**` passes. Smoke script exits 0.

---

## Notes for the orchestrator

- **Read research knowledge docs** (`2026-04-17_research-*.md`) when compiling context for jobs 3, 4, 6 — they contain specific flags, syscalls, and gotchas that prevent the dev agent from stumbling.
- **Danger zone** from harness: `internal/scanner/libproc_darwin.go` — any change requires explicit user approval via steer.
- **Iron laws** from harness: the server tests MUST assert self-PID cannot be killed even via crafted URLs; atomic writes on state.json; constant-time token compare; no secrets in logs. Failing any of these → block the job.

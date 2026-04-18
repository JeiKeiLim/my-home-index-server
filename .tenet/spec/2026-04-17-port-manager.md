# Spec — Home Port Manager

Date: 2026-04-17 · Feature slug: `port-manager`
Interview: `.tenet/interview/2026-04-17-port-manager.md`
Design: `.tenet/visuals/2026-04-17-01-mockup-terminal-dark.html` (+ prototype `2026-04-17-05-prototype-core-flows.html`, architecture `2026-04-17-00-architecture.html`)

## 1. Purpose

A single-binary Go web dashboard that discovers every user-owned TCP listener
in the port-forwarded range 40000–40500 on the user's MacBook and exposes
them through one authenticated URL (`yourhost.example:40000`) with
management actions: visit, copy URL, copy cwd, rename (persistent label),
restart (from a killed-and-remembered entry), and kill (SIGTERM→SIGKILL).
Replaces the workflow of juggling `lsof`, `ps`, and history-search across
terminals.

## 2. Tech Stack (pinned versions)

| Layer | Choice | Version | Why / Source |
|---|---|---|---|
| Language | Go | `1.23+` | Single binary, excellent stdlib, cgo available for libproc |
| HTTP | stdlib `net/http` + `http.ServeMux` | Go 1.22+ pattern routing | No framework dependency |
| Templates | `html/template` + `embed.FS` | stdlib | Server-rendered HTML, one binary |
| Front-end interactivity | htmx | `2.0.x` (served from embedded `web/static/htmx.min.js`) | Polling swaps, no Node toolchain |
| Port enumeration | cgo + libproc (`proc_listallpids`, `proc_pidinfo PROC_PIDFDSOCKETINFO`) | macOS 10.5+ stable API | Research `research-macos-tcp-listener-enumeration` |
| Port enumeration fallback | `lsof` subprocess (build tag or runtime flag `--scanner=lsof`) | macOS system lsof | Same research |
| Process metadata | `github.com/shirou/gopsutil/v4` | `v4.26.3+` | Research `research-macos-process-metadata` |
| Env loading | `github.com/joho/godotenv` | `v1.5.x` | `.env` reader |
| Session signing | stdlib `crypto/hmac` + `crypto/sha256` | — | No external session lib needed |
| Tests | stdlib `testing` + `github.com/stretchr/testify/require` | `testify v1.9+` | — |
| E2E | Playwright (Node, in `tests/e2e/`) | Playwright 1.50+ | Invoked by eval stage 5 |
| Platform | macOS 26 arm64 (primary) | — | No other platforms supported |

Go module path: `github.com/JeiKeiLim/my-home-index-server` (placeholder — user can rename).

## 3. Project Layout

```
.
├── go.mod
├── .env.example
├── cmd/port-manager/main.go          # wiring: config → deps → http server
├── internal/
│   ├── config/config.go              # .env + flag parsing
│   ├── auth/auth.go                  # token compare, cookie sign, rate limit
│   ├── scanner/                      # port enumeration
│   │   ├── scanner.go                # interface + default selection
│   │   ├── libproc_darwin.go         # cgo implementation (build tag)
│   │   └── lsof.go                   # subprocess fallback
│   ├── inspector/inspector.go        # gopsutil-based metadata
│   ├── process/process.go            # kill, restart (os/exec detached)
│   ├── store/store.go                # labels + remembered JSON store
│   ├── server/
│   │   ├── router.go                 # routes + middleware
│   │   ├── handlers.go               # GET /, GET /ports, POST /kill, etc.
│   │   └── render.go                 # template registry
│   └── model/model.go                # Port, Remembered, ViewModel structs
├── web/
│   ├── static/                       # htmx.min.js, css, favicon
│   └── templates/                    # login.html, dashboard.html, _ports.html (htmx fragment)
└── tests/
    ├── integration/                  # Go integration tests (spawn real listeners, assert)
    └── e2e/                          # Playwright tests
```

## 4. API Endpoints

All routes go through middleware: `RequestID → Logger → Auth → RateLimit (login only) → Handler`.

| Method | Path | Auth | Content-Type | Description |
|---|---|---|---|---|
| GET | `/healthz` | none | text/plain | Returns `ok`. Used by uptime checks. |
| GET | `/login` | none | text/html | Login form. |
| POST | `/login` | none | form | Accepts `token=<value>`; on success sets `pm_session` cookie + 303 to `/` (or `?next=` param). On fail, re-render form with error; bump rate-limit counter. |
| POST | `/logout` | session | — | Clears cookie; 303 to `/login`. |
| GET | `/` | session **or** bearer | text/html | Full dashboard shell (header, toolbar, empty `<tbody hx-get="/ports" hx-trigger="load, every 2s" hx-swap="innerHTML">`, modals, toast stack). |
| GET | `/ports` | session **or** bearer | text/html | **htmx fragment** — just the `<tr>`/`<div.card>` rows, wrapped in a `<template>` that includes both views. Also returns the `<div class="cards">` content in the same response (htmx picks via `hx-swap-oob`). |
| GET | `/ports.json` | session **or** bearer | application/json | Machine-readable same data. `[{port, label, cmd, cwd, uptime_s, source, pid, listen_addrs, alive, remembered, remembered_id?}]`. |
| POST | `/kill/{port:int}` | session **or** bearer | — | Looks up PID for port, validates `port ∈ [40000,40500]` AND `pid ≠ self_pid`. Sends SIGTERM; if still alive after `KILL_GRACE_MS`, sends SIGKILL. Moves row to `remembered`. Returns the updated `/ports` fragment (htmx swap) or `{ok:true}` (Accept: json). |
| POST | `/restart/{remembered_id}` | session **or** bearer | — | Spawns the remembered `(command, cwd, env)` via `os/exec.Command` with `SysProcAttr{Setpgid:true}`, stdin/stdout/stderr → `/dev/null`. Fire and forget. Returns 202. |
| POST | `/rename/{port:int}` | session **or** bearer | form | Accepts `label=<value>` (0–64 chars, trimmed, any non-control Unicode). Looks up the row's `(cwd,command)` key, upserts label in store. Returns updated fragment. |

### Request/response contracts
- All mutation routes require `X-Requested-With: XMLHttpRequest` header OR a valid CSRF token in a hidden form field when using sessions. The htmx templates include the header automatically.
- Bearer-auth callers (scripts, curl) are CSRF-exempt but MUST send `Authorization: Bearer <token>`.
- Port-range validation rejects `port ∉ [40000,40500]` with `400 out-of-range`.
- Self-PID check rejects `pid == os.Getpid()` with `403 cannot act on self`.
- Rate-limit: 5 failed logins per remote IP per 15-min sliding window → `429` + 15-min lockout response. Successful logins reset the counter for that IP.

## 5. Data Model (persisted to `~/.port-manager/state.json`)

Single JSON file. **Atomic write**: write to `state.json.tmp`, fsync, rename over. A `sync.Mutex` guards all writes.

```jsonc
{
  "version": 1,
  "labels": {
    // key = sha256hex("<cwd>\x00<command>") truncated to 16 hex
    "a1b2c3d4e5f67890": {
      "cwd": "~/projects/blog",
      "command": "npm run dev",
      "label": "blog-dev",
      "updated_at": "2026-04-17T10:22:14Z"
    }
  },
  "remembered": [
    {
      "id": "01HXYZ...",             // ULID
      "port": 40123,
      "command": "python worker.py --queue hot",
      "cwd": "~/work/worker",
      "env": ["PYTHONPATH=~/work", "…"],  // KEY=VAL entries
      "killed_at": "2026-04-17T10:40:02Z",
      "label_key": "a1b2c3d4e5f67890"
    }
  ]
}
```

**Retention policy:** at most 5 `remembered` entries per `label_key`; drop oldest when exceeded. No TTL — entries persist across dashboard restarts.

**Tables** view:

| Entity | Key | Columns | Constraints |
|---|---|---|---|
| Labels | `sha256hex(cwd \x00 command)[:16]` | cwd, command, label, updated_at | label length 0–64; blank clears entry |
| Remembered | ULID | port, command, cwd, env[], killed_at, label_key | per-`label_key` retention 5; global cap 100 |

There is no SQL database. All state fits in one JSON file that loads in <10ms.

## 6. Design Direction

**Chosen mockup:** `.tenet/visuals/2026-04-17-01-mockup-terminal-dark.html`
**DESIGN.md:** `.tenet/DESIGN.md`
**Prototype:** `.tenet/visuals/2026-04-17-05-prototype-core-flows.html`

Key commitments:
- Dark terminal aesthetic, monospace throughout (all text).
- Palette via CSS custom properties (`--bg`, `--accent`, `--danger`, …). New code MUST use these tokens, not hex literals.
- Desktop: dense table (`<table>` zebra-striped). Mobile (<820px via single `@media`): swaps to stacked `<div class="cards">`.
- No loading spinners for 2s refresh (htmx does in-place swaps).
- Destructive actions (kill) require confirm modal. Rename uses a small modal with Enter-to-save + Escape-to-cancel. Restart requires confirm modal that shows the exact command+cwd about to be spawned.
- Toast stack bottom-center: 2s auto-dismiss, colour-coded by action kind.

## 7. Auth Flow

1. On startup, `config` reads `.env`:
   - `AUTH_TOKEN` (required; if empty, generate a 32-byte random token and **print it to stdout exactly once**, then persist the generated token back to `.env` so subsequent starts reuse it).
   - `PUBLIC_HOST` (default `yourhost.example`).
   - `PORT` (default `40000`).
   - `PORT_RANGE` (default `40000-40500`).
   - `KILL_GRACE_MS` (default `3000`).
   - `SESSION_SECRET` (required; auto-generated + persisted if missing).
2. Incoming request hits the `Auth` middleware.
3. **Bearer path:** header `Authorization: Bearer <token>` — constant-time compared to `AUTH_TOKEN`. Match → request proceeds with `authKind=bearer`; mismatch → 401.
4. **Cookie path:** cookie `pm_session` present → HMAC-verify; if valid and not expired (TTL 24h since issuedAt), request proceeds; else clear cookie + 303 to `/login?next=<original-url>`.
5. **Unauthenticated GET `/login`** → renders form.
6. **POST `/login`** with `token=<value>`:
   - Rate-limit check (5 failures / 15 min / remote IP). If locked, 429.
   - Constant-time compare against `AUTH_TOKEN`.
   - Success: issue signed cookie `pm_session = base64(issued_at || hmac(secret, issued_at))`, `HttpOnly; Secure=false (plain HTTP MVP); SameSite=Lax; Path=/`; 303 to `next` or `/`. Reset rate-limit counter for IP.
   - Failure: bump counter; re-render form with generic "invalid token" (no timing leak via message text).
7. `/logout` deletes the cookie and redirects to `/login`.

Session secret rotation: restart the server; new `SESSION_SECRET` invalidates all existing cookies.

## 8. Scanner Contract (port enumeration)

```go
type Scanner interface {
    Scan(ctx context.Context) ([]Listener, error)
}
type Listener struct {
    PID     int
    Port    int
    Addrs   []string  // e.g. ["0.0.0.0:40123", "[::1]:40123"]
    Source  string    // "libproc" | "lsof"
}
```

- `libproc_darwin.go` (default): cgo walk described in research. MUST short-circuit when `pbi_uid != os.Getuid()`. MUST skip own PID. MUST filter ports to range before allocating a `Listener`. Scan budget: ≤50ms for up to 500 own-user PIDs.
- `lsof.go`: shells `lsof -iTCP -sTCP:LISTEN -P -n -a -u $(id -u) -F pPn -b` with `ctx` bound to 1s timeout via `exec.CommandContext`. `cmd.Output()` only — never `StdoutPipe` without `Wait()`. Stderr discarded.
- `scanner.New(cfg)` selects impl based on `cfg.Scanner` (`"auto"` → `libproc` on darwin, else `lsof`).

## 9. Success Criteria (measurable, acceptance-testable)

1. **Discovery latency:** a process that opens port 40123 appears in the dashboard within **≤3s** of opening. (`2s refresh + render budget`.)
2. **Self-exclusion:** the dashboard's own PID is never shown in the table (not hidden, not present). Verified by scanning while the server is running.
3. **Kill works:** clicking "kill" on a row sends SIGTERM; the row disappears on the next scan within ≤3s of the process exiting. Acceptance test asserts this with a stub child that handles SIGTERM cleanly in <500ms.
4. **Kill escalates:** a child that ignores SIGTERM is SIGKILL'd after `KILL_GRACE_MS` (default 3s) ± 200ms. Acceptance test with `trap '' TERM` child process.
5. **Graceful kill does NOT hang the UI:** kill handler returns within 50ms (non-blocking; waits happen in a goroutine).
6. **Restart spawns detached:** clicking "restart" on a remembered entry causes the command to run, become a LISTEN socket on the same or a new port, and show up in the table. The dashboard is not the parent process group.
7. **Labels persist:** after rename + full dashboard restart (kill `./port-manager`, relaunch), the same `(cwd, command)` re-acquires the label.
8. **Atomic label write:** 100 concurrent rename requests never corrupt `state.json` (goroutine test with `-race`).
9. **Scanner timeout:** if `lsof` fallback is used and the process hangs, the request times out at 1s and returns an error banner — UI does not freeze.
10. **Auth:** request to `/` without cookie or bearer → 303 to `/login`. Request to `POST /kill/40123` without auth → 401. Request with wrong token → 401.
11. **Rate limit:** 5 failed logins from one IP in 15m → 6th attempt returns 429. A successful login resets the counter.
12. **Port-range guard:** `POST /kill/22` returns 400 (out of range). `POST /kill/443` returns 400.
13. **Responsive:** Playwright test at viewport 400×720 renders the mobile card layout (cards visible, table hidden).
14. **Copy actions:** client-side clipboard calls succeed on Chrome + Safari; toast reads `copied: http://yourhost.example:40123/` or `copied: ~/projects/blog`.
15. **No launch-new endpoint exists:** HTTP `POST /launch` returns 404; no code path constructs arbitrary commands from user input.

## 10. Out of Scope

- Launch-arbitrary-command feature.
- Log-tail / stdout capture / SSE log streaming.
- TLS termination (user fronts with Caddy/nginx or accepts plain HTTP).
- Multi-user access / per-user views.
- UDP listeners.
- Ports outside 40000–40500.
- Persistent historical port database (only labels + last-N killed remembered entries persist).
- Process CPU / memory charts.
- QR codes for URLs.
- Command-wrapper mode (managing stdio of dashboard-spawned processes) — noted as "next round".
- Running on Linux / Windows / other macOS versions.
- Authentication via OAuth / OIDC / SSO.
- launchd/brew services auto-start.

## 11. Observability

- Logs go to stderr in JSON (`log/slog`): `{"level":"info","ts":"…","msg":"kill","port":40123,"pid":84321,"signal":"TERM","grace_ms":3000}`.
- Scan metrics logged every 60s at `debug` level: `scan_ms`, `pids_walked`, `listeners_found`.
- No OpenTelemetry / Prometheus endpoint in MVP.

## 11.5 Test Fixtures & E2E Setup

### Unit / integration fixtures
- Spawning real child listeners in tests: helper `testutil.NewChild(t, cmd, port)` in `tests/integration/testutil/` creates a child `go run` of a tiny stub server that prints its PID and exits on SIGTERM (<500ms). Returns `PID`, teardown func.
- Stub child for kill-escalation tests: `testutil.NewStubbornChild(t, port)` runs a stub that traps SIGTERM (`signal.Notify` then drops the signal) so the test can assert SIGKILL escalation after `KILL_GRACE_MS`.
- Store tests use `t.TempDir()` as `HOME` via `t.Setenv("HOME", dir)`.

### E2E fixtures (Playwright)
- **Binary start:** `tests/e2e/playwright.config.ts` uses `webServer: { command: "./port-manager --port 40091", env: {AUTH_TOKEN: "e2etest-0123456789abcdef", SESSION_SECRET: "e2e-secret-0123456789abcdef0123", PORT: "40091", PUBLIC_HOST: "localhost"}, port: 40091, timeout: 10000 }`. Playwright auto-starts and tears down.
- **AUTH_TOKEN / SESSION_SECRET for e2e:** fixed constants `e2etest-0123456789abcdef` and `e2e-secret-0123456789abcdef0123`. Never used in production. `.env.e2e` sample committed at `tests/e2e/.env.e2e` for clarity; CI copies it.
- **Seeding a listener:** before each test, Playwright `globalSetup` in `tests/e2e/global-setup.ts` spawns a Python one-liner (`python3 -c "import http.server,socketserver;socketserver.TCPServer(('0.0.0.0',40192),http.server.SimpleHTTPRequestHandler).serve_forever()"`) and tears it down in `globalTeardown`. Tests can spawn additional listeners as needed via `child_process.spawn` with detached+ref.
- **Flaky-isolation:** each e2e test uses a unique port sub-range (40190–40199 reserved for e2e; tests pick a port based on `process.pid % 10`).
- **Login convenience:** a Playwright fixture `loggedInPage` POSTs to `/login` with `e2etest-0123456789abcdef` before returning a `Page`.

### Off-device CI
Running tests on non-darwin CI is **explicitly out of scope.** CI MUST run on `macos-14` or `macos-15` runners (arm64). The libproc cgo scanner has no Linux stub; attempting to cross-compile will fail by design. `.github/workflows/ci.yml` pins `runs-on: macos-15`.

## 12. Build / Run

```bash
# one-time
cp .env.example .env          # edit AUTH_TOKEN, PUBLIC_HOST
go build -o port-manager ./cmd/port-manager

# run
./port-manager                # uses .env; binds 0.0.0.0:40000
./port-manager --port 40010   # override

# scripts
curl -H "Authorization: Bearer <token>" http://yourhost.example:40000/ports.json
```

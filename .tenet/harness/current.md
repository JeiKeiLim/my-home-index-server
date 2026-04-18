# Harness: Quality Contract — port-manager

Project: home-port-manager · Stack: Go 1.23+ · Platform: macOS 26 arm64
Spec: `.tenet/spec/2026-04-17-port-manager.md`
Scenarios: `.tenet/spec/scenarios-2026-04-17-port-manager.md`
Design: `.tenet/DESIGN.md`

## Formatting & Linting
- **Go code formatter:** `gofmt -s` (stdlib). CI rejects files that differ.
- **Go static analysis:** `go vet ./...` must pass.
- **Extra lints:** `golangci-lint` **pinned at `v1.62.x`** (install via `go install github.com/golangci/golangci-lint/cmd/golangci-lint@v1.62.2`). Run as `golangci-lint run --timeout=60s` with `errcheck, govet, staticcheck, ineffassign, gosec, unused, revive` enabled. Version pin lives in a `tool.go` file under `tools/` using Go 1.22+ tools directive (or `.golangci.yml` `run.go.version: '1.23'`).
- **HTML/CSS:** no automated formatter; follow DESIGN.md conventions (monospace only, CSS variables over hex literals).
- **enforcement:** pre-commit hook OR `make lint`; eval Stage 1 (mechanical) fails the job if any lint fails.

## Build
- `go build -o port-manager ./cmd/port-manager` MUST succeed with `GOOS=darwin GOARCH=arm64 CGO_ENABLED=1`.
- **Single binary.** No embedded directory must be expected to exist at runtime — all web assets via `embed.FS`.
- Reproducible build flag: `-trimpath -ldflags "-s -w"` for release builds.

## Testing Requirements
- **Framework:** stdlib `testing` + `github.com/stretchr/testify/require`.
- **Coverage:** ≥ **80%** line coverage on new code in `internal/**`. Excluded from coverage: `cmd/`, `internal/scanner/libproc_darwin.go` (cgo boundary; covered by integration tests only), `web/templates/*`.
- **Unit tests:** MUST run with `go test -race -shuffle=on ./...`.
- **Integration tests** (`tests/integration/`): spawn real child processes listening on real ports in a per-test sub-range, exercise kill/restart/scan.
- **E2E tests** (`tests/e2e/`, Playwright): cover S1, S2, S3, S6 at minimum. Run against a locally started binary with a fixed test AUTH_TOKEN and seeded labels.
- **Race detector MUST be clean.** `go test -race ./...` exits 0.
- **Flakiness budget:** if a test fails 1 in 20 runs, it is flaky — fix it, don't retry.

## Smoke check (Stage 1.5)
After build:
1. `./port-manager --port 40010 --public-host localhost` in the background, with a test `AUTH_TOKEN=smoketest`.
2. `curl -sf http://localhost:40010/healthz` returns `ok` within 3s.
3. `curl -sf -H "Authorization: Bearer smoketest" http://localhost:40010/ports.json` returns JSON `[]` or a valid array.
4. Kill the process; verify clean exit (`$?` == 0 or 143 for SIGTERM).

A smoke-check failure = Stage 1 failure regardless of unit test results.

## Architecture Rules

1. **cgo isolated to `internal/scanner/libproc_darwin.go`** with build tags. No other file may `import "C"`.
2. **Handlers are thin.** HTTP handlers in `internal/server/handlers.go` do routing + form parsing + call into `scanner`, `inspector`, `process`, `store`. They do NOT implement business logic. Rule enforced by review; no handler file over 400 lines.
3. **No global mutable state** except `config` (read-once) and the singleton `store` and `scanner`. All other state is injected via constructors.
4. **All I/O paths have context.** Every function that performs syscall / network / subprocess takes `ctx context.Context` and respects cancellation.
5. **Templates are pre-parsed at startup** via `template.ParseFS(web.TemplatesFS, ...)`. No lazy parsing per-request.
6. **HTML output MUST use `html/template`.** No `template.HTML()` casts anywhere; forbidden by lint.
7. **Scanner results are immutable.** A scanner run produces a snapshot; handlers never mutate the snapshot.
8. **Restart spawns with detached pgid.** `SysProcAttr{Setpgid: true, Setsid: true}`; stdin/stdout/stderr → `/dev/null`.
9. **Every `exec.Cmd` MUST `.Wait()` or be reaped.** No fire-and-forget `.Start()` without a reaper goroutine.
10. **No process is killed by the dashboard unless the scanner snapshot lists it** in the current range AND `pid != os.Getpid()` AND `uid == getuid()`.

## Code Principles
- Prefer composition over inheritance (Go-style embedding kept minimal).
- Explicit over implicit — no `init()` side effects except for template parsing.
- Functions do one thing. 50-line ceiling for handlers; 100-line ceiling for everything else unless the whole function is a switch/table.
- `errors.Is`/`errors.As` for error matching; wrap with `fmt.Errorf("...: %w", err)`.
- No `panic()` in request paths; middleware recovers and returns 500.
- Log with `slog`, not `log`. Structured keys, not sprintf.

## Danger Zones (do not modify without explicit user approval)
- `internal/scanner/libproc_darwin.go` — cgo syscall glue; easy to introduce memory-corruption bugs. Changes here require an explicit interview/steer directive.
- `internal/auth/auth.go` — any change must be paired with updated acceptance tests for A6, A7, A18, A19, A20.
- `~/.port-manager/state.json` (at runtime) — never read or write outside `internal/store`.
- `.env` (user's file) — read only at startup; never written to except by the one-shot `AUTH_TOKEN` + `SESSION_SECRET` auto-generation path.

## Iron Laws (invariants that must always hold)

1. **Self-preservation:** `os.Getpid()` is NEVER a valid kill target. Enforced at the kill handler AND the scanner (filters it out). Two guards, not one.
2. **Same-uid only:** all listeners surfaced and all processes actioned MUST satisfy `uid == getuid()`. No exceptions.
3. **Port-range guard:** every action route validates `port ∈ [config.PortMin, config.PortMax]` server-side. UI disables is NOT enforcement.
4. **Constant-time token compare:** `subtle.ConstantTimeCompare` for `AUTH_TOKEN`. Never `==` or `bytes.Equal`.
5. **No shell interpolation:** restart and any subprocess call uses `exec.Command(name, args...)` with the stored argv slice. No `sh -c`, no `strings.Join`.
6. **Scan budget:** every scan call is `ctx` with 1s timeout. On timeout, return stale snapshot + `X-Scan-Error: timeout`; do not block the handler.
7. **Atomic state writes:** store writes go through `writeAtomic(path, data)` that writes to `path.tmp`, `fsync`, `rename(path.tmp, path)`. No direct `os.WriteFile`.
8. **No secret in logs:** middleware redacts `Authorization`, `Cookie: pm_session`, `AUTH_TOKEN`, `SESSION_SECRET` from slog output.
9. **CSRF guard:** mutation routes check `X-Requested-With: XMLHttpRequest` (htmx) OR valid CSRF token (non-htmx form fallback) for cookie-auth'd clients; bearer-auth bypasses.
10. **No `template.HTML()` / `safehtml.HTMLEscaped`** — enforced by a codebase grep lint.
11. **Zero CGO on non-darwin:** scanner fallback (`lsof.go`) MUST compile without `CGO_ENABLED=1`. Conditional build tags enforced.
12. **HTTP server timeouts set:** `ReadTimeout 5s, WriteTimeout 10s, IdleTimeout 60s, ReadHeaderTimeout 2s`. No unbounded timeouts.
13. **State file atomic cap:** JSON state file size MUST stay ≤ 256 KiB; enforced by eviction policy (per-key retention 5, global cap 100 remembered entries).

## Definition of Done (per job in this feature)
1. Code compiles with `go build ./...`.
2. `go vet ./...` clean.
3. `golangci-lint run` clean.
4. `go test -race -shuffle=on ./...` passes.
5. Any new public function has a godoc one-liner.
6. Any new route is listed in the spec API table AND has an acceptance test.
7. No Iron Law violated.
8. If the job touches HTML/CSS, DESIGN.md tokens are used (no raw hex).

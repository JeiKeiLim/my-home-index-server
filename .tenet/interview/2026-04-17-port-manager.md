# Interview: Home Port Manager

Date: 2026-04-17
Mode: Full
Rounds: 4
Feature slug: port-manager

## Clarity Score
- Goal: 0.85 (weight 0.4)
- Constraints: 0.80 (weight 0.3)
- Success criteria: 0.85 (weight 0.3)
- **Total: 0.835 / 0.8 required — PASSED**
(Scored independently by `tenet_validate_clarity`, 2026-04-17.)

### Gaps noted (to resolve in spec, not re-interview)
- Quantify: dashboard response latency budget, refresh interval, max tracked processes.
- Pin Go version (e.g. `go 1.23+`).
- Finalize login flow: bearer + password→cookie both enabled; cookie preferred UX.
- Pick external-log-attach strategy: `log stream --predicate 'processID == X'` as primary, graceful fallback text if stream fails or produces nothing within 2s.
- Ring buffer memory budget: 1000 lines × avg ~200B ≈ 200 KB/process. Confirm in spec.
- Login rate-limit: 5 failed attempts per IP per 15 min → lockout 15 min.
- `lsof` subprocess timeout: 2s; fail-soft with "scan timed out — retry" banner.
- Replay history retention: last 5 per (cwd, command).
- SIGTERM→SIGKILL default grace: 3s, acceptance test asserts this.
- Refresh cadence: 2s htmx polling; acceptance test asserts ≤3s row-disappear after kill.

## Problem statement (verbatim from user)
> My home network setup is using yourhost.example with port forward to this MacBook
> which ranges from 40000 to 40500. I tend to open up some servers within that
> range. It's getting unmanageable — what did I open, where was that port opened
> from? Instead of jumping back and forth, I'd like a single endpoint that
> collects any port within that range, where it is running, what command was run,
> and manages them (kill, visit-link, plus any other useful management tools).

## Round 1

### Questions Asked
1. **Tech stack?** (categories: Technical Constraints)
   > User asked for clarification on Go for frontend; after explainer (Go + htmx
   > = single binary with server-rendered HTML), confirmed **Go + htmx** in Round 2.

2. **Auth model — yourhost.example is public?** (Security)
   > **Password/token login.** Shared secret via env var / .env.

3. **Historical tracking or live only?** (Data)
   > **Live only.** Each refresh re-scans. No DB.

4. **Extra management features beyond list/kill/visit?** (Scope, UX)
   > Selected: tail stdout/stderr log, restart last command (replay), rename/label
   > a port, and public-URL **copy button** (QR was declined).

## Round 2

### Questions Asked
1. **Concrete stack shape?** (Tech Constraints)
   > **Go + htmx + server-rendered HTML**, single binary.

2. **UI style?** (UX)
   > **Dense table with inline actions** — one row per port, columns:
   > `port | command | cwd | uptime | [kill] [visit] [logs] [rename]`.

3. **Deployment model?** (Tech Constraints, Edge Cases)
   > **Run manually from terminal.** No launchd / brew services. User starts it
   > when they want it. (NOTE: this means the tool itself is ephemeral — build
   > must be simple to start: `./port-manager` with a `.env` nearby.)

4. **Edge cases in MVP?** (Edge Cases)
   > Selected: **exclude port-manager's own port** from the list, and
   > **graceful kill (SIGTERM → wait → SIGKILL)**.

## Round 3

### Questions Asked
1. **Dashboard's own listen port?** (Integration)
   > **Fixed port in range** — default 40000, configurable via `--port` flag
   > or `.env`. Always reachable at `yourhost.example:40000`.

2. **Which processes to track?** (Scope, Data)
   > **TCP listeners owned by the current user, bound to 0.0.0.0 / :: /
   > 127.0.0.1, in range 40000–40500.** UDP excluded.

3. **Log capture strategy for tail / replay?** (Data, Integration, Edge Cases)
   > **Capture for all processes with graceful degradation.** For processes
   > launched *through* the dashboard, full stdout/stderr ring buffer. For
   > *external* processes (launched outside dashboard), attempt to attach
   > (macOS `log stream`, or similar best-effort), and if that fails, show
   > "logs unavailable — launch via dashboard to capture" in the UI. Never
   > crash or hang when attach fails.

4. **Public URL derivation?** (Integration)
   > **`.env` file** with a `PUBLIC_HOST` setting, default `yourhost.example`.
   > The "visit" button renders `http://{PUBLIC_HOST}:{PORT}`.

### Decisions Made
- **Language + runtime:** Go (latest stable), single static binary.
- **UI:** server-rendered HTML templates + htmx for live refresh and actions;
  embed templates/CSS/JS via `embed.FS`.
- **Auth:** single bearer token (env var, e.g. `AUTH_TOKEN`) OR a shared password
  stored hashed in `.env` → session cookie. Anonymous requests bounce to a
  login page. Configurable which one.
- **Port range:** 40000–40500. Dashboard itself defaults to 40000.
- **Process discovery:** `lsof -iTCP -sTCP:LISTEN -P -n` (or equivalent
  `net.Listen` syscall probing) to enumerate listeners in range, filtered by
  current `uid`. Skip the dashboard's own PID.
- **Process metadata per port:** PID, executable, full command line (ps -o
  command), working directory (`lsof -p PID -Fcwd` or `proc_pidinfo`), start
  time, listen address (IPv4/IPv6/both), uptime.
- **Kill:** SIGTERM, wait configurable grace period (default 3s), then SIGKILL
  if still alive. Report which signal ended it.
- **Visit button:** copies or opens `http://{PUBLIC_HOST}:{PORT}/`.
- **Copy button:** copies the public URL to clipboard (client-side JS).
- **Rename/label:** labels persisted to a local JSON/TOML file keyed by
  `(cwd, command)` (so a label survives process restart at the same port).
  Rename DOES NOT survive a full port-change of that cwd+command.
- **Logs (dashboard-launched):** ring buffer (default 1000 lines) in memory,
  streamed via SSE or htmx polling.
- **Logs (external process):** best-effort only. If attach fails, UI shows
  "not capturing — relaunch via dashboard".
- **Replay:** remembers last N (cwd, command, env snapshot) per port; one-click
  "replay" re-spawns via `os/exec` detached from the terminal (new process
  group, stdout/stderr to ring buffer).
- **Persistence:** in-memory state only for process info; labels + replay
  history on disk (JSON file, default `~/.port-manager/state.json`).
- **Self-exclusion:** dashboard filters its own PID from the list, or shows it
  with kill disabled.

### Success criteria (measurable scenarios)
1. User runs `./port-manager` and hits `yourhost.example:40000` — login page appears.
2. After login, dashboard shows a dense table of every user-owned TCP listener
   in 40000–40500, with port / command / cwd / uptime / actions.
3. User clicks "kill" on a port — process receives SIGTERM, then SIGKILL after
   grace period if still alive; row disappears on next refresh (≤3s).
4. User clicks "visit" — browser opens `http://yourhost.example:PORT/` in a new tab.
5. User clicks "copy" — `http://yourhost.example:PORT/` is copied to clipboard.
6. User launches a new process VIA the dashboard (`launch` action with command
   + cwd) — it appears in the table, and clicking "logs" shows its stdout.
7. User clicks "rename" on a port, types a label — label persists across a
   manual kill + relaunch of the same command+cwd.
8. User clicks "replay" on a killed-but-remembered port — the exact command
   restarts and the port reappears within seconds.
9. Dashboard's own row is hidden (or shown with disabled kill).
10. External process (launched outside dashboard) shows metadata; clicking
    "logs" shows "not capturing — relaunch via dashboard" without error.

### Anti-scenarios (must NOT happen)
- Dashboard is reachable without auth from yourhost.example.
- Clicking "kill" on the dashboard's own PID takes the dashboard down.
- Attach-to-external-logs failure crashes the dashboard or hangs requests.
- Refreshing the page leaks file descriptors from repeated `lsof` spawns.
- A slow/stalled `lsof` blocks the entire UI (must have timeout).
- Labels file write corrupts on concurrent rename (must use atomic write).
- Replay spawns into the parent terminal (must detach and set new pgid).
- Port-manager misidentifies another user's process as its own and kills it.
- Auth token printed to logs / shown in the UI.
- Ports outside 40000–40500 appear in the table.

### Remaining Ambiguities
- **Login flow:** bearer token (header) vs password → cookie. Default will
  be password → cookie (better UX on mobile); bearer token available as
  `Authorization: Bearer …` for curl/scripts. Final call during spec.
- **Rate limits on login:** defer to harness (simple per-IP lockout ok).
- **HTTPS:** dashboard serves plain HTTP; TLS termination is the user's
  responsibility (Caddy/nginx in front, or we add a `--tls-cert` flag later).
  MVP = plain HTTP.
- **Exact log-attach strategy for external processes:** `log stream
  --predicate 'processID == X'` works but is lossy and needs Unified Logging.
  Alternative: scrape `/dev/ttys*` — fragile. Will be scoped in research +
  spec. Graceful-degradation guarantee remains regardless of strategy chosen.

## Round 4 — Mockup review & scope trim

After reviewing the 4 mockup variations, the user picked **01 terminal-dark**
and requested the following changes, which cascade into feature scope:

### Changes requested
1. **Mobile responsive design required.** The mockup now collapses into a
   stacked card view below 820px; touch-sized action buttons.
2. **Rename "replay" → "restart"** throughout. More intuitive verb.
3. **Drop the "launch new" feature.** The dashboard never spawns arbitrary
   new commands. The user continues to start servers from their own terminal.
4. **Add a "copy CWD" button/affordance per row.** Desktop: inline `⎘`
   icon next to the cwd text. Mobile card: full-width "copy cwd" button.
5. **Drop the logs / log-tail feature entirely.** Cascade from (3):
   - macOS research confirmed external-process stdout capture is infeasible
     without root or debugger entitlements.
   - With no "launch new", log capture would only ever work for
     restart-spawned processes — a tiny use case.
   - User noted: "I'm thinking about having you as command wrapper so you can
     manage stdio, but that's for the next round and has flaws since I might
     be running docker compose up -d kind of commands." Deferred.

### Knock-on consequences locked in
- **No stdout/stderr ring buffer.** Removes SSE broker, `io.Pipe`,
  per-process log capture goroutines.
- **No log-attach logic for external processes.** Removes the entire
  graceful-degradation path around `log stream` / pty / ptrace.
- **Htmx polling only.** No SSE stream needed — whole dashboard is 2s
  polled table swap.
- **Restart remains, but is rare-use.** Re-spawns a *previously-seen*
  killed process with its original `(command, cwd)` via `os/exec`
  detached — fire and forget. No stdout capture. UI shows "restart
  submitted" toast; the process either reappears on the next 2s scan or
  it doesn't (user can check their terminal / the process's own logs).
- **Remembered-but-not-live rows** remain in the table only as long as a
  restart is plausible (retention: last 5 killed per `(cwd, command)`
  key, wiped on dashboard restart — matches interview Round 1 "live only"
  answer for port data; labels+restart metadata still persist via JSON).

### Questions asked (design confirmation)
1. > Which of the 4 mockups? (with previews)

   > Terminal-dark (01). Needs mobile responsive + rename replay→restart.
2. > Drop launch-new and keep restart?

   > Drop launch. (Later clarified: drop logs too, since they'd only work
   > for restarted processes.)

### Final features locked in
| Feature | In MVP? | Notes |
|---|---|---|
| List live TCP listeners 40000–40500 (own-uid) | ✅ | Every 2s via htmx |
| Per-row metadata (port, cmd, cwd, uptime, source) | ✅ | gopsutil + cgo libproc |
| Kill (SIGTERM → 3s → SIGKILL) | ✅ | Never kills self |
| Visit URL | ✅ | Opens `http://{PUBLIC_HOST}:{PORT}` |
| Copy URL | ✅ | Client-side `navigator.clipboard` |
| Copy CWD | ✅ | Client-side copy of absolute path |
| Rename / label (persisted) | ✅ | JSON file, `(cwd, command)` key |
| Restart (from remembered) | ✅ | `os/exec` detached, fire-and-forget |
| Self-exclusion | ✅ | Dashboard's own PID disables kill/rename/restart |
| Filter (external / captured / text search) | ✅ | htmx swap |
| Auth (token + cookie) | ✅ | `.env`-configured |
| Mobile responsive (< 820px stacked cards) | ✅ | Single HTML, CSS breakpoint |
| Launch-new command | ❌ | Dropped in Round 4 |
| Log tail / stdout capture | ❌ | Dropped in Round 4 |
| TLS termination | ❌ | Plain HTTP; user fronts with Caddy if needed |
| UDP / non-TCP | ❌ | TCP only |
| Persistent port history DB | ❌ | Labels only persist |
| Command wrapper (stdio managed by dashboard) | ❌ | Noted as "next round" |

## Summary

Build a single-binary **Go + htmx** dashboard that runs manually on the user's
MacBook (macOS 26 arm64), listens on a fixed port in the 40000–40500 range
(default 40000), and presents a **responsive auto-refreshing table** (desktop
dense table / mobile stacked cards) of every user-owned TCP listener in that
range. Each row shows `port / label / command / cwd / uptime / source` plus
**`visit / copy url / copy cwd / rename / restart / kill`** actions.

- **Auth:** password → session cookie (+ bearer token for scripts), via `.env`.
- **State:** live-scan every 2s; in-memory only for port table; labels +
  last-N-killed restart entries persisted to `~/.port-manager/state.json`.
- **Kills:** graceful SIGTERM → 3s → SIGKILL; dashboard never kills itself.
- **Restart:** fire-and-forget `os/exec` of a previously-seen `(cmd, cwd)`;
  no stdout capture.
- **No logs feature.** Ship without stdout ring buffers / SSE / attach-to-PID.
- **Public URL:** `.env`-configured `PUBLIC_HOST`, default `yourhost.example`.
- **Deployment:** user starts manually via `./port-manager`. No launchd/Docker.
- **Responsive:** < 820px breakpoint collapses the table into mobile cards.

Out of scope for MVP: launch-new command, log tail/stdout capture, TLS
termination, multi-user access, UDP, ports outside 40000–40500, persistent
history DB, process CPU/mem stats, QR codes, command-wrapper stdio.

# Scenarios & Anti-Scenarios — port-manager

Date: 2026-04-17 · Feature: `port-manager`
Spec: `.tenet/spec/2026-04-17-port-manager.md`

## Success Scenarios (the feature MUST deliver these)

### S1 — First-time login and discovery
1. User runs `./port-manager` in a terminal.
2. Output prints `listening on 0.0.0.0:40000` and (if AUTH_TOKEN was empty) a one-time generated token.
3. User opens `http://yourhost.example:40000` on their phone or laptop.
4. Login page appears; user pastes the token and submits.
5. Dashboard shell loads. Within 3s the table populates with every TCP listener the user owns in 40000–40500.
6. Dashboard's own row (port 40000) is **absent** from the list.

**Outcome assertions:** 200 on `/`, session cookie set, scan returns ≥1 row (seed a listener in the test).

### S2 — Kill a runaway dev server
1. User's Next.js dev server is eating memory on port 40102.
2. User clicks the `kill` button on the row.
3. Modal appears showing `npm run dev` + `~/dev/blog`.
4. User clicks `SIGTERM → SIGKILL`.
5. Kill handler returns <50ms; the Next.js process receives SIGTERM and exits within 500ms.
6. Within 2s the next scan removes port 40102 from the live list.
7. The row moves to `remembered` (visible via `restart history` toolbar button). Its command, cwd, and env are preserved for restart.

**Outcome assertions:** row `alive=false`, `remembered=true`, `label_key` still points at the saved label.

### S3 — Restart a remembered entry
1. User opens the `restart history` drawer.
2. Clicks `restart` on the `flaky-worker` entry.
3. Confirm modal shows `python worker.py --queue hot` + `~/work/worker` + env count.
4. User confirms.
5. Dashboard spawns the command detached via `os/exec` with a new process group.
6. Within 3s the new process opens a TCP listener in range; the row reappears as `alive=true, source=captured`, and is removed from `remembered`.
7. The dashboard is NOT the parent of the new process (verified by `ps` — new PGID).

**Outcome assertions:** `os.Getpgid(newPID) != os.Getpid()`, listener visible via scanner.

### S4 — Rename a label
1. User clicks `rename` on the row for port 40301 (uvicorn, unlabeled).
2. Dialog opens. User types `demo-api`. Presses Enter.
3. Toast `label saved: demo-api` appears.
4. Row's label column updates immediately.
5. User kills the uvicorn process (now remembered).
6. User restarts the remembered entry. The new row shows `demo-api` without re-entering it.
7. User restarts `./port-manager` itself. Uvicorn is still running. The row still shows `demo-api`.

**Outcome assertions:** `~/.port-manager/state.json` has a `labels` entry for the hashed `(cwd, command)` key; after restart, hydrated store contains it.

### S5 — Copy URL + copy CWD
1. User hovers row for port 40123.
2. Clicks `copy url`. Toast `copied: http://yourhost.example:40123/` appears. Clipboard contains that URL.
3. Clicks the inline `⎘` next to the cwd. Toast `copied: ~/notebooks`. Clipboard contains the absolute path (the display ellipsis `~/notebooks` is expanded before copying).

**Outcome assertions:** navigator.clipboard receives the full URL and absolute path.

### S6 — Mobile responsive layout
1. User opens dashboard on their phone (viewport 390×844).
2. Table is hidden. Stacked cards visible. Each card has full-width-wrapping action buttons.
3. Header's `token rotated` meta is hidden (too narrow).
4. Buttons are ≥44pt tall (Apple HIG) by virtue of the 7×10 padding + 12px font.
5. Kill confirm modal fits the viewport without horizontal scroll.

**Outcome assertions:** Playwright screenshot at 390×844 shows no table, cards visible.

### S7 — Auth guard on mutation routes
1. An unauthenticated curl hits `POST /kill/40123`.
2. Response: 401 Unauthorized.
3. With bearer token: same request succeeds (if port is live) or 404 (if not).

**Outcome assertions:** 401 body contains no secrets; 200/404 paths work with bearer.

### S8 — Rate-limit lockout
1. User botches the token 5 times from the same IP within 15 minutes.
2. 6th attempt (even with correct token) returns 429 for 15 minutes.
3. After the window, the counter resets; a successful login works.

**Outcome assertions:** `Retry-After` header present on 429.

### S9 — Scanner graceful degradation
1. Running with `--scanner=lsof` on a machine where `/Volumes/StaleMount` causes `lsof` to hang.
2. The 1-second context timeout fires.
3. `GET /ports` returns an error banner in the HTML: `scan timed out — retry`. UI stays responsive, header/toolbar unaffected.
4. Subsequent scans succeed once the stall clears.

**Outcome assertions:** 200 response but `X-Scan-Error: timeout` header.

---

## Anti-Scenarios (the feature MUST prevent these)

### A1 — Kill-self
A bug or UI exploit lets the dashboard send SIGTERM to its own PID, taking itself down. **Prevented by:** server-side guard in kill handler comparing `pid == os.Getpid()` → 403; UI also disables the button for the self-row.

### A2 — Cross-user kill
A malicious client crafts `POST /kill/40123` when the LISTEN socket on 40123 is owned by another user (edge case if user switched). **Prevented by:** scanner already filters to `pbi_uid == getuid()`; kill handler re-resolves PID via scanner (not via request body) and asserts uid match.

### A3 — Command injection via rename label
User types `"; rm -rf ~ ;"` as a label. **Prevented by:** labels are data-only; never interpolated into a shell. Restart uses `exec.Command(argv[0], argv[1:]...)` with the stored argv slice, not a shell command string.

### A4 — CWD escape via symlink manipulation
Restart uses a cwd like `~/../../etc` that escapes the user's expected tree. **Prevented by:** stored cwd is the *original* resolved path captured at kill time via `proc_pidinfo`; no user-editable cwd field exists.

### A5 — State file corruption on concurrent rename
Two rename requests race; `state.json` ends up truncated or invalid JSON. **Prevented by:** `sync.Mutex` around all writes + atomic tmp+rename + fsync. Test with `go test -race` running 100 concurrent renames.

### A6 — Auth bypass via missing bearer / empty token
Request with empty `Authorization: Bearer ` (empty token after space). **Prevented by:** constant-time compare against empty-string AUTH_TOKEN is rejected before the compare (we reject tokens of length < 16 outright); AUTH_TOKEN cannot be empty on startup (empty triggers generation of a 32-byte random token).

### A7 — Session cookie replay after server restart
`SESSION_SECRET` changes; an old cookie should no longer authenticate. **Prevented by:** HMAC verify with current secret; mismatch → 303 login.

### A8 — Out-of-range port kill via crafted URL
`POST /kill/80` from a clever client. **Prevented by:** handler validates port ∈ `[CFG.PortRangeMin, CFG.PortRangeMax]` → 400.

### A9 — UDP listener treated as TCP
Scanner picks up a UDP socket and tries to kill it as if it were HTTP. **Prevented by:** libproc walker filters `soi_kind == SOCKINFO_TCP` AND `tcpsi_state == TSI_S_LISTEN`. lsof fallback passes `-iTCP -sTCP:LISTEN`.

### A10 — Dashboard-blocking slow scan
`lsof` hangs and blocks the next `GET /ports` request, which blocks rendering, which blocks the htmx polling loop, which shows a frozen UI. **Prevented by:** every scan call is `ctx.WithTimeout(1s)`; the HTTP handler returns a minimal error fragment immediately on timeout; the table keeps showing the previously-rendered rows.

### A11 — Fd leak from repeated lsof invocations
`exec.Cmd` with `StdoutPipe` never `Wait()`ed → pipe fd leak. **Prevented by:** scanner uses `cmd.Output()` which internally waits and closes pipes. Test asserts `/dev/fd` count is stable after 1000 scans.

### A12 — Zombie child accumulation from restart
`os/exec.Command` started without `Wait()` leaves zombies. **Prevented by:** restart spawner starts the command, then launches a reaper goroutine that calls `cmd.Wait()` (so the dashboard reaps, but doesn't track exit status or logs — fire-and-forget). Alternative: use `syscall.SysProcAttr{Setsid: true}` and let the init system reap.

### A13 — XSS via command name in dashboard
A process with `<script>alert(1)</script>` as argv is shown in the command cell. **Prevented by:** all template output uses `{{.}}` (html/template auto-escapes). No `template.HTML()` casts in the codebase. Acceptance test asserts script tags are escaped.

### A14 — Copy-URL uses wrong host
Dashboard host is bound to `0.0.0.0` but user accesses via `localhost`; the copy-URL button still returns `yourhost.example:PORT`. **Prevented by:** the generated URL ignores the `Host` header of the incoming request and uses `PUBLIC_HOST` from config. This is intentional — copy-url is for sharing the public link.

### A15 — Label file write partial-fill on crash mid-write
Power loss during write leaves `state.json` zero-length. **Prevented by:** atomic write = write full content to `state.json.tmp`, `fsync(tmp)`, `rename(tmp, state.json)`. `rename` is atomic on HFS+/APFS.

### A16 — Restart spawns into the dashboard's terminal
The spawned process inherits the dashboard's stdin/stdout, so "npm run dev" ANSI output pollutes the server console. **Prevented by:** stdin/stdout/stderr set to `os.DevNull` (open `/dev/null` and assign); `SysProcAttr.Setpgid: true` and `Setsid: true` where available.

### A17 — Unbounded remembered-list growth
User kills 1000 different processes; `state.json` balloons. **Prevented by:** per-`label_key` retention cap of 5 + global cap of 100 entries; oldest dropped on insert.

### A18 — Login password shown in URL
POST /login with password in query string accidentally. **Prevented by:** form uses `method="POST" action="/login"` and input type is `password`; GET /login never accepts credentials.

### A19 — CSRF on /kill from a phishing page
User has a valid session cookie; a third-party page submits `POST /kill/40102` via an `<img>` or form. **Prevented by:** all mutation routes require `X-Requested-With: XMLHttpRequest` (htmx includes this automatically) OR a CSRF form token (for non-htmx forms). Plain `<img>`-driven requests don't include either.

### A20 — Token printed to logs
AUTH_TOKEN accidentally logged by an error handler or HTTP middleware. **Prevented by:** middleware redacts `Authorization`, `Cookie`, and any `.env`-sourced secrets. Test greps server stderr for known token value after every request — must be absent.

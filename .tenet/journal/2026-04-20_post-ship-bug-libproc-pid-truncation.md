# post-ship-bug-libproc-pid-truncation

type: journal
source_job: post-ship-bugfix
job_name: libproc scanner pid-table truncation
created: 2026-04-20T04:30:00.000Z

## Findings

- **classification**: Post-ship bug. Shipped in v1 (commit 4aaa700, 2026-04-18). Reported by real user on 2026-04-20 — "I'm not seeing 55100 in the dashboard even though I can connect to 55100 port externally."
- **symptom**: On a real dev machine with ~800 live processes and a port range of 55000-55500, the dashboard showed only the port-manager itself. Two vite dev servers bound to 55100 and 55101 (same uid, 0.0.0.0 bind, visible to `lsof`) were missing.
- **root_cause**: `listAllPIDs()` in `internal/scanner/libproc_darwin.go` divided the `proc_listallpids` return value by `sizeof(pid_t)`, assuming it was a byte count. The libproc wrapper actually returns the pid count directly. Result: only ~1/4 of the pid table was iterated, and any listener whose pid fell past position `N/4` was silently skipped.
- **diagnosis_path**:
  1. Verified running process had fresh build and tracked `.env` (ruled out stale config).
  2. Confirmed lsof CLI sees the listeners (uid 501, same as port-manager).
  3. Wrote a throwaway `cmd/scanner-diag` that ran both libproc and lsof implementations in-process: libproc returned 1, lsof returned 3. **This pinned the bug to the cgo walk.**
  4. Probed each step of the walk: `proc_pidinfo(PROC_PIDTBSDINFO)` worked for all targets; `proc_pidfdinfo(PROC_PIDFDSOCKETINFO)` returned LISTEN state correctly when called on a specific pid. The missing step was that the targets weren't in the pid list the scanner iterated.
  5. Instrumented `proc_listallpids` with varied buffer sizes: `slots=100 → n=100`, `slots=500 → n=500`, `slots=2000 → n=798`. Return value matched slot count until kernel ran out of pids — definitive proof the return is a count, not bytes.
- **fix**: 11-line change in `listAllPIDs` — stop dividing by `int32Size`, size the buffer in slots not bytes.
- **regression_guard**: Added `TestListAllPIDs_NoTruncation` (cross-checks against `ps -A`), plus `export_darwin_test.go` to expose `listAllPIDs` to the `_test` package.
- **verification**: After rebuild and restart, user confirmed dashboard showed 55100 and 55101. Scanner-diag reported 3/3 listeners on both paths.
- **commit**: `fb83217 fix(scanner): treat proc_listallpids return as pid count, not bytes` on main, 2026-04-20.

## Lessons for the tenet pipeline itself

- **eval_gap_1**: `playwright_eval` and the integration tests both used spawned-in-test processes. The kernel often placed the fresh child pid near the top of the table, landing in the preserved quartile by chance. Tests passed; production failed on the same code. **Action**: any scanner/enumerator test needs a fixture that forces the target into a late position (e.g. sleep a few seconds after spawn so the OS reshuffles, or spawn many decoys first).
- **eval_gap_2**: `code_critic` reviewed the arithmetic but had no way to verify the libproc return convention without running it. The Apple header comment is ambiguous. **Action**: for any cgo/syscall wrapper, require an empirical probe script (committed, runnable) alongside the implementation — the probe becomes both documentation and a drift detector.
- **eval_gap_3**: This bug would have been caught immediately by a single "does my real dev machine match expected output?" manual smoke. The automated smoke test in `.tenet/harness/current.md` §Smoke check runs against a clean binary with no other user processes. **Action**: add an optional "user-environment smoke" step — run scanner on the dev's real system, compare count against `lsof | grep LISTEN` count.
- **knowledge_debt_paid**: Created `.tenet/knowledge/2026-04-20_proc_listallpids-return-convention.md`. The earlier `2026-04-17_research-macos-tcp-listener-enumeration.md` described the correct syscall sequence but did not capture the return-value trap — exactly the class of knowledge gap pre-spec research is supposed to catch.

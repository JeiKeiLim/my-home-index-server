# proc_listallpids-return-convention

type: knowledge
source_job: post-ship-bugfix
job_name: libproc-scanner-truncation-fix
confidence: implemented-and-tested
created: 2026-04-20T04:30:00.000Z

## Findings

- **discovered_during**: Post-ship bug report on 2026-04-20. User's real home deployment (port range 55000-55500) — externally-reachable vite dev server on port 55100 did not appear in the dashboard even though `lsof` saw it. Reproduced: libproc scanner returned only 1 of 3 user-owned listeners; lsof fallback returned all 3.
- **finding**: `proc_listallpids(buf, bufsize)` on darwin returns the **count of pids written**, NOT the byte count. The ambiguous Apple doc comment says "number of bytes"; empirical measurement on macOS 26 contradicts that on the user-space wrapper.
- **empirical_proof**: On a 794-process system, asking with slots=100 returned 100; slots=500 returned 500; slots=2000 returned 798 (buffer had 797 non-zero entries); slots=10000 returned 798. The cap is always the slot count (or the real pid count when buffer is larger), never bytes.
- **contrast_PROC_PIDLISTFDS**: Same syscall family (`proc_pidinfo(pid, PROC_PIDLISTFDS, ...)`) returns BYTES, confirmed: 200-slot / 1600-byte buffer returned 416 which equals 52 fdinfos × 8 bytes. Do not unify the two conventions mentally — they differ.
- **bug_that_shipped**: `internal/scanner/libproc_darwin.go:listAllPIDs` did `count := n / int32Size`, silently truncating the pid table to ~1/4. The bug was invisible on a quiet system (few pids, all early in the table) and on the single-listener integration test (kernel pid ordering is non-deterministic, the freshly-spawned test child often landed in the preserved quartile). It only manifested on a real dev machine with many long-lived processes.
- **fix**: Treat the return value as slice length directly; size the buffer with `count + padding` slots (not `count / int32Size + padding`). Fix committed 2026-04-20 (see journal 2026-04-20_post-ship-bug-libproc-pid-truncation).
- **regression_test**: `TestListAllPIDs_NoTruncation` in `internal/scanner/integration_darwin_test.go` cross-checks against `ps -A -o pid=` and requires ≥ half the pid count. Broken code returns ~1/4 and fails the test.
- **why_eval_gates_missed_it**: code_critic reviewed the arithmetic but had no way to verify the API contract without empirical test. test_critic reviewed tests but the existing integration test only spawns one python listener — kernel pid ordering non-determinism meant the fresh pid was often in the preserved quartile, so the test passed. playwright_eval never exercises the port range with arbitrary pre-existing listeners. **Lesson for future tenet runs**: any cgo/syscall wrapper whose return-value semantics aren't unit-testable from pure Go NEEDS an empirical probe committed alongside, not just a code review.
- **related_gotchas_to_check**: For every future `proc_*info` call, write a small probe that tests three buffer sizes (undersized, exactly-sized, oversized) and records the observed return-value convention in a knowledge doc BEFORE shipping. Candidates to audit now:
  - `proc_listpids(PROC_UID_ONLY, ...)` — may behave differently than `proc_listallpids`
  - `proc_listpidspath(...)` — untested in this project
  - `proc_pidinfo(pid, PROC_PIDPATHINFO, ...)` — untested
- **primary_sources**: https://opensource.apple.com/source/xnu/ (libsyscall/wrappers/libproc/libproc.c) — but source reading alone is not sufficient; versions drift. Empirical probe is authoritative.
- **cross_reference**: Updates earlier research note `2026-04-17_research-macos-tcp-listener-enumeration.md` which described the correct syscall sequence but did not mention the return-value trap.

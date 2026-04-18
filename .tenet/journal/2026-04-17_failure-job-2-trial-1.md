# failure-job-2-trial-1

type: journal
source_job: 5b7f7e07-914d-4d82-8495-7fb6e38b728d
job_name: config + auth
created: 2026-04-17T02:30:59.849Z

## Findings

- **trial**: 1
- **job**: job-2 config + auth
- **verdict**: code critic FAILED (5 blockers); test critic PASSED with 3 sharpenings; playwright N/A
- **code_blockers**: 1) Rate-limiter failures map grows unboundedly — pruneLocked only runs on revisit, IP-rotation attacker can OOM. Need bounded cap / LRU / periodic janitor. 2) persistGenerated writes .env via os.WriteFile+os.Rename with no fsync of tmp (or parent dir) before rename — crash can lose the one-time generated AUTH_TOKEN after banner scrolls (also violates Iron Law 7 spirit). 3) firstNonZero(opts.Port,...) silently replaces explicit Options.Port=0 with DefaultPort — callers meaning 'ephemeral' (antiscenarios_test does this) get DefaultPort back. Use *int sentinel or document clearly. 4) persistGenerated does not handle 'export KEY=' syntax supported by godotenv — will append duplicate AUTH_TOKEN= instead of replacing. 5) CheckBearer lacks explicit MinTokenBytes guard; short tokens rejected only incidentally via SHA256 mismatch. Add explicit len(token)<MinTokenBytes short-circuit (still safe because it tests untrusted input, not secret).
- **test_sharpenings_add_these**: a) Assert AUTH_TOKEN banner prints EXACTLY ONCE (not on subsequent Load). b) Tighten rate-limit lockout assertion to retryAfter>14min (not just <=window). c) Add test that recording failures while locked keeps IP blocked for full window.
- **next_approach**: Single retry addressing all 5 blockers. Do not create new job — scope was correct, implementation needs hardening. Keep the interface contracts and test suite; add the guards, atomic fsync, bounded map, sentinel Port, export-prefix handling, explicit short-token guard, and the 3 new tests.

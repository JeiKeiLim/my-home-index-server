# job-9-mutation-routes-complete

type: journal
source_job: b0ef895a-84ed-4c1e-ab1c-d494040a370f
job_name: mutation routes (/kill, /restart, /rename)
created: 2026-04-17T08:38:22.658Z

## Findings

- **job**: job-9 mutation routes
- **verdict**: passed on retry 2 after fixing store-before-kill ordering, rune/byte label mismatch, X-Scan-Error labeling, and restart modal heading
- **key_lessons**: 1) Persist-to-store comes AFTER the destructive syscall when the syscall can fail — otherwise a failure leaves phantom state. Env must be captured BEFORE the kill (Darwin strips envp post-exit) but the Remember() call itself waits for success. 2) When two layers both enforce 'max chars', make sure they mean the same thing — rune-based on BOTH, not 'rune on top, byte below'. Multibyte languages silently break. 3) Mirror error classification across request-path and mutation-path helpers that emit the same header. 4) Remembered entries must expose port (not just id) to the UI — users think in port numbers.
- **followup_notes**: Mobile-safari kill-restart-rename specs have a pre-existing .first() locator bug — compound 'tr,.card' selector picks hidden <tr> on mobile. Out of job-9 scope but worth cleaning up in job-10 polish or a follow-up.
- **acceptance_passing**: go test -race -shuffle=on ./... all green; desktop-chromium 9/9 Playwright tests pass (S2, S3, S4, S5, S5 toast, S6, S6 inverse); TestA8 + TestA17 + TestA19 pass; bearer CSRF bypass verified live; 64-rune unicode rename accepted; Restart modal shows 'Restart port N?'

# failure-job-9-trial-1

type: journal
source_job: b0ef895a-84ed-4c1e-ab1c-d494040a370f
job_name: mutation routes (/kill, /restart, /rename)
created: 2026-04-17T08:15:34.105Z

## Findings

- **trial**: 1
- **verdict**: code_critic FAILED 3; test_critic PASSED with gaps; playwright_eval FAILED S3 modal heading
- **blockers**: F1 (code, big): handleKillMutation calls store.Remember BEFORE process.Kill. Spec says kill then remember. No rollback on kill failure -> phantom restart-pending row for a still-live listener. Fix: capture env+argv+cwd into a local Remembered struct pre-kill, call Kill, then store.Remember(r) only on success. F2 (code): validateLabel uses utf8.RuneCountInString but store.SetLabel uses len() (bytes). 64-rune multibyte label passes handler, fails store with ErrLabelTooLong. Pick one (runes) and apply at both layers — spec says 'chars'. F3 (code): writePortsFragment sets X-Scan-Error: 'timeout' for EVERY error. handlePortsHTML correctly distinguishes DeadlineExceeded ('timeout') from other errors ('error'). Mirror that distinction. F4 (playwright): Restart modal heading shows 'Restart <remembered-id>?' because #restart-port span is populated with id. S3 expects 'Restart port N?'. Fix: template should be 'Restart port <span id=restart-port></span>?' and JS fills the span with the port number from the remembered entry.
- **test_gaps_to_add**: TestKillMutationEndToEnd should assert store.ListRemembered returns a new entry with pre-kill env snapshot; rename TestKillMutationRememberedCapEndToEnd to actually drive 6 POST /kill calls and assert <=5 remembered; explicit 'bearer bypass CSRF' test; 64-char exact-boundary test; TestRestartMutationSpawnsRememberedCommand should observe a ProcessManager.Restart call side-effect (fake records id/cwd/argv) not just 202.

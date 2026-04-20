---
name: run-stats tenet pipeline validation
description: Full timing + retry breakdown for the port-manager v1 autonomous build. Serves as a case-study data point for the tenet pipeline itself.
type: journal
---

# Run stats — port-manager v1 (tenet pipeline validation)

Generated post-ship on 2026-04-18. Data source:
`.tenet/.state/tenet.db` (committed).

## Wall clock

| | |
|---|---|
| First tenet action | `2026-04-17 08:54:45 KST` |
| DAG complete (e2e-final pass) | `2026-04-17 21:56:46 KST` |
| **Total span** | **13.0 hours** |
| User-requested sleep windows (3× Claude quota resets) | ~3.5 h (71m + 35m + 108m) |
| **Autonomous active time** | **≈ 9.5 hours** |

## DAG shape

- **14 real jobs** — 12 core DAG (scaffold → config+auth → scanner → inspector
  → store → e2e-1 → process → server-shell → dashboard-UI → mutation-routes →
  polish → e2e-final) plus 2 ad-hoc fixes (XFF rate-limit isolation,
  mobile-safari locator + htmx-stub replacement).
- **74 auxiliary jobs** — 24 code-critic + 24 test-critic + 24 playwright-eval
  (three critics per real job, dispatched in parallel), plus 2 integration
  critics and 2 readiness / clarity validators.
- **88 total jobs, 0 blocked.**

## Retry pressure

| trials → pass | jobs |
|---|---|
| **4 trials (3 retries, worst)** | `fix-mobile-safari-locator-bug` |
| 3 trials (2 retries) | `polish`, `final integration` |
| 2 trials (1 retry) | `config+auth`, `scanner`, `inspector`, `store`, `server-shell`, `dashboard-UI`, `mutation-routes` |
| 1 trial (no retry) | `scaffold`, `process`, `e2e-1` checkpoint, XFF-isolation fix |

- **10 of 14 real jobs needed at least one retry (71%).**
- 13 total retry attempts across the run.

## Heaviest real jobs (dispatch → result)

These times are wall-clock from `tenet_start_job` to `tenet_job_result`, so
they include polling idle gaps, user-requested sleeps that overlapped the
window, and worker agent execution time. They are **not** pure agent-CPU.
Cross-job the absolute wall clock stays 13 h.

| job | dispatch→result | retries |
|---|---:|---:|
| final integration: full acceptance | 9.0 h | 1 |
| polish (README/release/CI/smoke) | 8.4 h | 2 |
| mutation routes | 7.7 h | 1 |
| dashboard UI | 6.9 h | 1 |
| server shell | 5.3 h | 1 |
| process manager | 4.7 h | 0 |
| store | 4.3 h | 1 |
| inspector | 3.6 h | 1 |
| mobile-safari fix (ad-hoc) | 3.2 h | 3 |

## Signal for tenet development

1. **Retry rate 71% is high** — suggests first-pass code_critic is
   appropriately strict. Most retries were single-shot fixable with
   targeted guidance.
2. **3 retries on the mobile-safari cleanup** — the worker kept emitting
   false-pass summaries (summary claimed "29/0 pass" while on-disk
   artifacts showed failures). **Proposed tenet improvement:** the retry
   prompt builder should *require* raw artifact attachment (last-run.json,
   full loop output) in the summary for flake-prone test classes. Trusting
   narrative summaries is a reliable way to waste retries.
3. **No jobs hit blocked state** — decomposition was sound; the
   orchestrator never needed to self-steer to unblock a dep chain.
4. **Eval gate caught substantive bugs**, not cosmetic ones. Concrete
   examples from this run:
   - Store job declared `Remembered.Command string` instead of the
     `[]string` from the decomposition interface contract — would have
     broken restart round-trip at job-6 wire-up.
   - Kill handler persisted `store.Remember` **before** `process.Kill`
     returned, leaving a phantom restart-pending entry when Kill failed.
   - Missing CSRF guard on `POST /logout`.
   - Vendored htmx stub dispatched `htmx:afterSwap` on trigger instead of
     target, breaking all click-initiated OOB mobile swaps.
   - `app.js` fired `htmx.trigger('refresh')` but `#ports-body` only
     listened for `load, every 2s` — `refresh` was a no-op; mutations
     waited up to 2 s for the next poll.
5. **Sleep/resume checkpoint worked cleanly** — 3 distinct paused windows
   totaling 3.5 h, resumed via `tenet_continue()` each time with zero
   state loss. The durable SQLite state survives full Claude session
   tear-down.

## Knowledge artifacts produced during the run

- `.tenet/knowledge/2026-04-17_research-macos-tcp-listener-enumeration.md`
- `.tenet/knowledge/2026-04-17_research-macos-process-metadata.md`
- `.tenet/knowledge/2026-04-17_research-macos-external-process-log-capture.md`
- `.tenet/knowledge/2026-04-17_macos-26-stripped-envp-crossprocess.md`
- `.tenet/knowledge/2026-04-17_config-auth-hardening-checklist.md`

Every failure journal + every pass journal is in `.tenet/journal/` and
references the DB job id, so a future agent can `tenet_continue` + read
the journals to pick up either the project or lessons for adjacent work.

## Post-ship addendum (2026-04-20)

A sixth observation belongs on this case-study data point:

6. **One post-ship bug escaped the DAG.** On 2026-04-20 the user reported
   that listeners bound past the first ~1/4 of the pid table were
   silently missing from the dashboard. Root cause: `listAllPIDs` in the
   libproc cgo scanner treated `proc_listallpids`'s return as bytes and
   divided by sizeof(pid_t). The integration test for the scanner
   passed because it spawned a single python child whose pid usually
   landed in the preserved quartile by kernel-ordering luck. Fixed in
   commit `fb83217`. Write-ups:
   - `.tenet/journal/2026-04-20_post-ship-bug-libproc-pid-truncation.md`
   - `.tenet/knowledge/2026-04-20_proc_listallpids-return-convention.md`

   **Tenet pipeline lesson:** cgo/syscall wrappers whose return-value
   semantics aren't unit-testable from pure Go need an empirical probe
   committed alongside the implementation. Apple's header comment for
   `proc_listallpids` is ambiguous; source reading alone is not
   authoritative because conventions drift across xnu versions. The
   pre-spec research phase correctly identified the syscall sequence
   but did not probe return-value conventions — future decomposition
   DAGs for syscall-heavy features should include a dedicated
   "convention probe" task per syscall before the implementer job.
   This adds ~1 job to the DAG but would have caught this bug before
   v1 shipped.

## Raw queries reproducible

```sh
sqlite3 .tenet/.state/tenet.db <<'SQL'
SELECT datetime(MIN(timestamp)/1000, 'unixepoch', 'localtime'),
       datetime(MAX(timestamp)/1000, 'unixepoch', 'localtime'),
       printf('%.2f h', (MAX(timestamp)-MIN(timestamp))/1000.0/3600.0)
FROM events;

SELECT type, COUNT(*), printf('%.1f min', AVG(completed_at - created_at)/60000.0)
FROM jobs WHERE status='completed' AND completed_at IS NOT NULL
GROUP BY type ORDER BY AVG(completed_at - created_at) DESC;

SELECT SUM(CASE WHEN retry_count>0 THEN 1 ELSE 0 END) AS retried,
       SUM(retry_count) AS attempts, COUNT(*) AS total
FROM jobs;
SQL
```

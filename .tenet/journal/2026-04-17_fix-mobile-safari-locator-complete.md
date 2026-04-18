# fix-mobile-safari-locator-complete

type: journal
source_job: fd87471d-812e-4922-80df-426e5de63134
job_name: fix-mobile-safari-locator-bug
created: 2026-04-17T12:57:23.219Z

## Findings

- **job**: fix-mobile-safari-locator-bug (ad-hoc cleanup)
- **verdict**: passed on retry 3/3 after 2 false-pass-claim failures + 1 architecture fix + 1 dead-code cleanup
- **key_wins**: (1) Replaced broken vendored htmx stub with real htmx 2.0.4 — foundational fix for <template>-wrapped OOB mobile cards. (2) Found and fixed a second real bug: app.js calls htmx.trigger('refresh') but #ports-body hx-trigger only listens for 'load, every 2s' — the refresh() was a no-op. Added forcePortsRefresh() helper that drives htmx.ajax directly. (3) Introduced deterministic waitForPort(page, port) / waitForPortGone(page, port) helpers that poll /ports.json directly with back-off — eliminates the 2s-poll + 1s-scan race against tight toBeVisible budgets. (4) Fixed waitForPortGone to return false (not true) on non-200/non-array responses so transient errors don't race subsequent DOM assertions. (5) Removed dead portRow / drainOOBTemplates exports.
- **verification**: 5/5 consecutive runs of 'cd tests/acceptance/e2e && rm -rf test-results && npx playwright test --workers=1 --retries=0' — each reports 29 passed / 3 skipped / 0 failed. Zero flakes across 145 test invocations on both desktop-chromium and mobile-safari projects.
- **lesson_for_future**: (a) Never trust a vendored stub for a production JS library — browsers have subtle dispatch semantics that handwritten stubs routinely get wrong (e.g., afterSwap on trigger vs target). Vendor the real minified file. (b) Race-prone UI tests should gate DOM assertions on an API boundary, not rely on timeout generosity. waitForPort(page, port) polling /ports.json is the correct shape for any htmx-polled UI. (c) htmx.trigger(el, 'refresh') only fires if hx-trigger actually listens for 'refresh' — which the default 'load, every 2s' does NOT. Use htmx.ajax() for manual refreshes. (d) Retry prompts must insist on raw verification output in the summary — twice the worker claimed 'all pass' when artifacts showed fail.

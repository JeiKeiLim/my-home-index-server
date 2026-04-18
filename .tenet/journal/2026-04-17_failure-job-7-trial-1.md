# failure-job-7-trial-1

type: journal
source_job: 9c03a90e-b2f7-4426-b44b-312b26810161
job_name: server shell (router + login + middleware + templates)
created: 2026-04-17T05:54:04.792Z

## Findings

- **trial**: 1
- **verdict**: code_critic FAILED 5 findings; test_critic PASSED; playwright PASSED
- **blockers**: F1 (biggest): POST /logout lacks CSRF guard — runtime probe confirms cookie-auth POST with no X-Requested-With succeeds with 303 instead of 403. Spec §4 says all mutation routes using sessions require X-Requested-With or CSRF token. Dashboard logout form is plain HTML; either wrap /logout in withCSRF and make the form htmx-driven, or add a hidden CSRF token field. F2: static file server exposes embed.FS directory listing at GET /static/ — wrap handler to reject trailing-slash. F3: dead next= branch in requireAuthOrRedirect — route is GET /{$} so r.URL.Path is always '/'. Either broaden route or remove branch. F4: redactCookieHeader hard-codes leading space in replacement; when pm_session is first cookie, rewritten header gets stray leading space. F5: testhelpers stringRedacted is unused and silenced with var _ — use it or delete.
- **other_tests_passed**: Go anti-scenarios A6/A7/A20 pass; Playwright login.spec.ts desktop-chromium 6/6, mobile-safari 6/6 (isolated)

# job-7-server-shell-complete

type: journal
source_job: 9c03a90e-b2f7-4426-b44b-312b26810161
job_name: server shell (router + login + middleware + templates)
created: 2026-04-17T06:11:26.604Z

## Findings

- **job**: job-7 server shell
- **verdict**: passed on retry 2 after fixing CSRF-on-logout + 4 minor items
- **followup_noted**: Playwright exploratory found a UX nit: htmx logout button clears the cookie but doesn't navigate because the browser follows the 303 response before htmx parses HX-Redirect. Headers are correct; behavior is partial. Consider in job-9 or later: return 200 + HX-Redirect for XHR callers instead of 303, OR have the browser-side logout click use a plain form POST + CSRF token. Not blocking this job.
- **acceptance_passing**: go test -race ./... all green; Playwright login.spec.ts 6/6 desktop-chromium + 6/6 mobile-safari; A6/A7/A20 all pass; smoke verified /static/ 404, cookie POST /logout 403 without XHR / 303 with, bearer logout 303, X-Request-Id on every response

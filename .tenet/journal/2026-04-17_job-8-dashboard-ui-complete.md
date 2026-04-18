# job-8-dashboard-ui-complete

type: journal
source_job: 28f0756b-139f-4690-8623-c470be77a720
job_name: dashboard UI (GET /, /ports, /ports.json)
created: 2026-04-17T07:45:23.359Z

## Findings

- **job**: job-8 dashboard UI
- **verdict**: passed on retry 2 after fixing CSS tokens + mobile foster-parenting + selector
- **key_lesson_foster_parenting**: HTML5 parsing rules FORBID non-table elements as direct tbody children (DIV, etc.). When htmx swaps a fragment containing <tr> + <div id='rows-mobile' hx-swap-oob='true'> into a tbody target, the browser foster-parents the <div> out of the tbody, breaking OOB routing and producing two #rows-mobile elements in the DOM. FIX: wrap OOB blocks in <template> — template.content is parsed in a neutral context, avoiding foster-parenting, and htmx can still walk the template for [hx-swap-oob] attributes via a small drainOOBTemplates() helper hooked into htmx:afterSwap.
- **deliverables_added**: --row-self and --row-restart tokens in style.css :root + DESIGN.md palette; <template> wrapper in _ports.html; drainOOBTemplates() in app.js; label key assertion in TestPortsJSONShape; toast banner E2E; S6 inverse desktop test; S1 [data-port]+toHaveCount(2) selector
- **acceptance_passing**: go test -race -shuffle=on ./... all green; A13+A14 pass; Playwright copy-and-mobile 5/5 + login 6/6 per-file; mobile cards visually render at 390x844; table:none at mobile / block at desktop; computed styles match 820px breakpoint from both sides
- **documented_out_of_scope**: S2/S3/S4 kill/restart/rename handlers return 501 with // job-9 comment — expected to pass after job-9; cross-project webServer rate-limit cascade is pre-existing test harness state unrelated to this fix

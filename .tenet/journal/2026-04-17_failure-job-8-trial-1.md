# failure-job-8-trial-1

type: journal
source_job: 28f0756b-139f-4690-8623-c470be77a720
job_name: dashboard UI (GET /, /ports, /ports.json)
created: 2026-04-17T07:14:03.604Z

## Findings

- **trial**: 1
- **verdict**: code_critic FAILED 1 finding; test_critic PASSED; playwright_eval FAILED 1 major blocker + 1 scripted bug
- **blockers**: F1 (code): web/static/style.css lines 147-148 use undocumented hex literals #0d1016 (tr.self td background) and #141026 (tr.restart-pending td background). Violates DESIGN.md 'all CSS via tokens, no hex literals' rule. Fix: add --row-self and --row-restart to :root, document in DESIGN.md palette, replace hex with var(). F2 (playwright CRITICAL): mobile cards foster-parented into tbody. The /ports fragment contains <tr> rows AND <div id='rows-mobile' hx-swap-oob='true'> in the same response. When htmx swaps into tbody#ports-body, HTML5 foster-parenting rules move the <div> up to the nearest valid ancestor — resulting in TWO #rows-mobile elements (one 0x0 inside tbody, one empty in <main>). Mobile UI is visually blank under 820px breakpoint, breaking S6. F3 (playwright minor): S5 copy-cwd desktop fails on webkit because inline ⎘ click target selector mismatch.
- **scan_blockers_out_of_scope**: S2/S3/S4 kill-restart-rename and S8 rate-limit failures belong to later jobs, not job-8.
- **oob_fix_options**: (A) Wrap OOB <div> in <template> (htmx still parses OOB from template.content; avoids foster-parenting). (B) Change the poll target from <tbody hx-get='/ports'> to an enclosing div#ports-container that wraps both table and mobile cards; swap its innerHTML; CSS handles viewport visibility. (C) Two separate polls (doubles scan cost). Recommend (A) — minimal template change.
- **css_fix_details**: Add to :root in style.css: --row-self: #0d1016; --row-restart: #141026; document both in DESIGN.md Color palette table. Replace selectors to var(--row-self) / var(--row-restart).

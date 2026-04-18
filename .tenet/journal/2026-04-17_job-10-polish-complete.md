# job-10-polish-complete

type: journal
source_job: efdb85c0-df6b-4466-a0bd-1cfac6592657
job_name: polish (README, release, CI, smoke)
created: 2026-04-17T09:12:30.791Z

## Findings

- **job**: job-10 polish
- **verdict**: passed on retry 2 after fixing Go-version inconsistency + README CI prose drift
- **lesson**: When tightening CI, also update the prose that describes it. Single source of truth: always regenerate or manually sync the README description of the workflow after changing ci.yml. Similarly, pin Go versions consistently across go.mod, CI GO_VERSION, and README — GOTOOLCHAIN auto-download hides the drift at runtime but misleads users.
- **deliverables**: README with install/run/config-vars table/screenshot/Caddy+TLS guide/AUTH_TOKEN rotation/deliberately-does-NOT section; .github/workflows/ci.yml on macos-15 with gofmt+vet+lint+test+build+release+arm64-verify+smoke+playwright+report-upload; Makefile build/test/lint/smoke/e2e/release; scripts/smoke.sh with --version sentinel assertion; go.mod with toolchain go1.24.1 directive; docs/screenshot.png captured authentic dashboard

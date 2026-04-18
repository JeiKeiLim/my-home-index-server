# failure-job-10-trial-1

type: journal
source_job: efdb85c0-df6b-4466-a0bd-1cfac6592657
job_name: polish (README, release, CI, smoke)
created: 2026-04-17T08:57:19.647Z

## Findings

- **trial**: 1
- **verdict**: code_critic FAILED 1 finding; test_critic PASSED with gaps; playwright PASSED
- **blocker**: go.mod declares 'go 1.24.0', CI workflow pins GO_VERSION 1.23.x, README says 'Go 1.23+'. Three-way inconsistency. CI only works via implicit GOTOOLCHAIN auto-download of 1.24 at runtime (brittle, ~60MB extra). README misleads users on 1.23. Fix: bump CI GO_VERSION to 1.24.x AND README to 'Go 1.24+' (1.24 already declared in go.mod by gopsutil/cgo deps, can't lower). Also add toolchain directive to go.mod for determinism.
- **test_critic_gaps_add_now**: smoke.sh should assert ./port-manager --version output differs from 'port-manager dev' (catches dropped -ldflags silently reverting); CI should run 'make release' + verify file dist/port-manager-darwin-arm64 | grep arm64 (catches broken GOOS/GOARCH).

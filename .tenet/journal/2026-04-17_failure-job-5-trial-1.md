# failure-job-5-trial-1

type: journal
source_job: 8b8e450a-2cc4-4aef-9208-c03620f7e3ba
job_name: store (labels + remembered, atomic JSON)
created: 2026-04-17T04:39:57.981Z

## Findings

- **trial**: 1
- **verdict**: code_critic FAILED 3 findings; test_critic PASSED; playwright N/A
- **blockers**: F1: Remembered.Command is string, but the decomposition interface contract says []string and both inspector.ProcInfo.Command and process.Spec.Command use []string. Shell-join round-trip would break args-with-spaces. Change to []string, update LabelKey derivation to sha256hex(cwd\x00 + strings.Join(command,\x00))[:16], update all tests. F2: Iron Law 13 256 KiB cap only evicts Remembered; if Labels alone exceed 256 KiB the store silently writes oversized file. Add per-label cap OR surface an error when empty-Remembered still exceeds MaxStateBytes. F3: ListRemembered / FindRemembered copy outer Remembered by value but Env []string (and Command []string once fixed) still points at store's backing array — callers who mutate r.Env[0] trigger an unprotected race on store memory. Deep-copy both slices before returning.
- **lesson**: Always cross-check interface contracts across DAG job specs BEFORE implementing. The spec JSON sample used a single string for command, but the decomposition Go struct signature is the authoritative API. Also: slice fields in returned structs need deep copy if the doc comment claims 'caller may mutate freely'.

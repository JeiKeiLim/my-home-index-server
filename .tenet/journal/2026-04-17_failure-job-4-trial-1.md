# failure-job-4-trial-1

type: journal
source_job: a55b7d61-a326-485d-9067-aab736683bb5
job_name: inspector (gopsutil + cgo envp helper)
created: 2026-04-17T04:14:21.298Z

## Findings

- **trial**: 1
- **job**: job-4 inspector
- **verdict**: code_critic FAILED 4 findings; test_critic PASSED with 3 missing tests flagged; playwright N/A
- **blockers**: F1: classify() uses anonymous-interface pattern (interface{Errno() uintptr}) but syscall.Errno doesn't expose Errno() method — branch is dead code, errors.As always false. Replace with 'var e syscall.Errno; if errors.As(err, &e) { switch e { case syscall.EPERM: ...; case syscall.ESRCH: ... } }'. Package comment promises ErrPermission for cross-uid; today delivery depends on fragile string matching only. F2: readEnv maps syscall.EINVAL to ErrNotFound alongside ESRCH — EINVAL can be bad mib or bad buflen, not just 'pid gone'. Narrow to ESRCH or add comment proving EINVAL is specifically pid-gone for KERN_PROCARGS2. F3: TestReadEnvNonexistentPID asserts only err!=nil — add errors.Is(err, ErrNotFound). Plus add a cross-uid EPERM test that asserts errors.Is(err, ErrPermission). F4: classify() hard-codes numeric errnos 1/3 instead of syscall.EPERM/syscall.ESRCH constants.
- **test_critic_missing**: Live cross-uid ErrPermission via PID 1 env path; End-to-end CmdlineSlice->Cmdline+splitCmdline fallback branch; classify's errno/gops.ErrorProcessNotRunning branches.
- **next_approach**: Retry trial 2. Keep scope. Replace anonymous-interface errno pattern with typed syscall.Errno switch. Narrow EINVAL. Add errors.Is assertions to existing tests. Add live-EPERM test that attempts inspection of PID 1 (launchd is cross-uid) — if uid matches uid 0 only, assert ErrPermission on env path; if running as root in CI, skip.

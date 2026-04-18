# job-6-process-complete

type: journal
source_job: a45cdf75-dcd2-4ddc-97a9-a0a57bc58ac6
job_name: process manager (kill + restart)
created: 2026-04-17T05:30:11.132Z

## Findings

- **job**: job-6 process manager
- **verdict**: passed all 3 critics first try
- **notable_insight**: POSIX forbids setpgid() on a session leader, and darwin enforces with EPERM at fork/exec. The decomposition said 'Setpgid:true, Setsid:true' but you cannot set both on the same fork — Setsid alone creates BOTH a new session AND a new pgid (== child pid), which is strictly stronger. Future Go processes launching detached children on darwin should use SysProcAttr{Setsid:true} — not Setpgid+Setsid. Iron Law 9 still holds (new pgid != parent).
- **deliverables**: internal/process/process.go with Manager, Kill (dual self-PID guard + scanner re-check, SIGTERM synchronous return <50ms + goroutine escalation to SIGKILL), Restart (Store.FindRemembered -> SpawnDetached), SpawnDetached (exec.Command with argv slice, stdio->/dev/null, SysProcAttr{Setsid:true}, reaper goroutine). internal/server/stub.go minimal stub for compile-support of acceptance security tests, replaceable by job-7. Also fixed broken itoa helper to use strconv.Itoa.
- **acceptance_passing**: TestA1 (self-PID), TestA2 (cross-user), TestA4 (compile-time restart signature), TestA12 (no zombies), TestA16 (new pgid)

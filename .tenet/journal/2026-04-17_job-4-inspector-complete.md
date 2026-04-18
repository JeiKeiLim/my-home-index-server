# job-4-inspector-complete

type: journal
source_job: a55b7d61-a326-485d-9067-aab736683bb5
job_name: inspector (gopsutil + cgo envp helper)
created: 2026-04-17T04:25:17.040Z

## Findings

- **job**: job-4 inspector
- **verdict**: passed on retry 2 after failing trial 1 on classify() dead-code anonymous-interface errno pattern + EINVAL over-narrowing
- **lesson**: Go's stdlib exposes errnos as syscall.Errno (underlying uintptr) — use `var e syscall.Errno; errors.As(err, &e); switch e { case syscall.EPERM: ... }`, NOT an anonymous interface{Errno() uintptr}. The interface pattern compiles but never matches because syscall.Errno does not expose an Errno() method. Second lesson: sysctl EINVAL is ambiguous (bad mib / bad buflen / pid gone); disambiguate with a targeted syscall.Kill(pid, 0) probe to cleanly classify as ErrNotFound/ErrPermission/other. Third: when a public API promises to classify errors, the tests must assert errors.Is against the sentinel — 'err != nil' is too weak.
- **discovery**: Documented separately in 2026-04-17_macos-26-stripped-envp-crossprocess.md. Impacts restart feature (job-6): cross-process env is stripped on macOS 26; must fall back to filtered current env.

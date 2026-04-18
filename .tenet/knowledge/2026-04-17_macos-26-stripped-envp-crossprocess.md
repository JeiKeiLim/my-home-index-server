# macos-26-stripped-envp-crossprocess

type: knowledge
source_job: a55b7d61-a326-485d-9067-aab736683bb5
job_name: inspector (gopsutil + cgo envp helper)
confidence: implemented-and-tested
created: 2026-04-17T03:42:52.997Z

## Findings

- **discovered_during**: job-4 inspector implementation and integration tests on macOS 26.0.1 arm64
- **finding**: macOS 26 strips envp[] from KERN_PROCARGS2 for CROSS-PROCESS reads as a hardening measure. Only the PROCESS READING ITS OWN procargs sees a populated env array. Other same-uid processes return empty env.
- **conflicts_with**: Earlier knowledge doc research-macos-process-metadata says same-uid reads work via KERN_PROCARGS2 with no entitlement. That remains true for argv, exec_path, and argc — but envp is now stripped even for same-uid cross-process reads on macOS 26.
- **impact_on_port_manager**: Restart feature (spec §7, job-6) expects to replay (cmd, cwd, env) of a killed process. We can capture env ONLY when we inspect a process we ourselves spawn (children of this process), OR when a process is self-reporting. For processes launched by the user in another terminal — the typical case — we cannot retrieve env. Workaround: at kill time, inherit the current dashboard's env minus a small curated allowlist for replay (PATH, HOME, USER, SHELL, LANG, PWD), OR accept that replay may behave differently from original launch. Users with env-sensitive commands (e.g. AWS_PROFILE set in a specific shell) will notice restart failing.
- **recommended_spec_update**: job-6 prompt and the restart scenario (S3) should note: env captured on a best-effort basis. If envp is unavailable (macOS cross-process hardening), fall back to current process env filtered to a safe subset. Document in dashboard UI 'restart may use current environment rather than original.'
- **verification**: Reproduced empirically on macOS 26.0.1 arm64. Self-inspection via KERN_PROCARGS2 returns populated envp; cross-process (same-uid) inspection of /bin/sleep child returned empty env.

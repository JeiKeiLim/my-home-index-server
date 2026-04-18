# research-macos-process-metadata

type: knowledge
source_job: 00000000-0000-0000-0000-000000000000
job_name: unknown
confidence: decision-only
created: 2026-04-17T00:05:23.294Z

## Findings

- **topic**: Retrieve per-PID cmdline/cwd/start-time/uid/env for same-uid processes on macOS 26 from Go
- **recommended_primary**: gopsutil v4 (github.com/shirou/gopsutil/v4/process). Under the hood it uses proc_pidpath + PROC_PIDVNODEPATHINFO + PROC_PIDTBSDINFO + sysctl KERN_PROCARGS2 — the same set Apple DTS recommends.
- **recommended_supplement**: One small cgo helper (~60 LOC) around sysctl KERN_PROCARGS2 to retrieve envp[], because gopsutil.Environ() returns 'not implemented yet' on Darwin in v4.26.3.
- **field_mapping**: Exe -> proc_pidpath ; Cmdline/CmdlineSlice -> KERN_PROCARGS2 ; Cwd -> PROC_PIDVNODEPATHINFO ; CreateTime (ms-since-epoch wall clock) -> PROC_PIDTBSDINFO.pbi_start_tvsec (use time.UnixMilli + time.Since for uptime) ; Uids -> PROC_PIDTBSDINFO.pbi_uid/ruid ; Environ (DARWIN GAP) -> write custom cgo KERN_PROCARGS2 parser
- **same_uid_perms**: None required. Public POSIX/Darwin syscalls (sysctl, proc_pidinfo) for same-uid inspection are NOT gated by SIP/TCC/Endpoint Security/App Management on macOS 26. Cross-uid reads return EPERM without root or com.apple.system-task-ports, but this feature only targets same-uid processes.
- **rejected_ps_cwd**: macOS ps does NOT support the 'cwd' keyword (returns 'ps: cwd: keyword not found'). BSD-on-Linux has it; macOS does not. Must use PROC_PIDVNODEPATHINFO or lsof.
- **lstart_format**: ps -o lstart= returns 'Fri Apr 17 08:59:57 2026' parseable with time.Parse('Mon Jan _2 15:04:05 2006', ...) but locale-sensitive. Prefer PROC_PIDTBSDINFO for robustness.
- **ps_argv_truncation_myth**: Classic 4096-byte truncation on 'ps -o command' did NOT reproduce on macOS 26 (measured 8182 and 60169 byte outputs with 'ps -ww'). Appears to be Red-Hat/Linux-specific folklore. But argv joined with spaces loses word boundaries for args containing spaces — still prefer KERN_PROCARGS2 which returns proper argv[] array.
- **rejected_launchctl_procinfo**: 'launchctl procinfo PID' returns 'This subcommand requires root privileges: procinfo' even for your own PIDs on macOS 26. Not viable for a normal-user daemon.
- **rejected_lsof_for_cwd**: lsof -a -p PID -d cwd -Fn works fine but costs ~40ms per call vs a sub-ms libproc call. Acceptable as fallback only.
- **decision**: gopsutil v4 + small cgo helper for envp. Avoid shelling to ps/lsof for per-PID metadata. If zero-cgo build desired, accept that Environ is unavailable (only other option is 'ps eww -p PID' which also space-joins env).
- **zero_cgo_flag_note**: If project opts for pure-Go build (drop all cgo), use gopsutil alone and mark Environ as 'unavailable on darwin'.
- **reference_files_during_research**: /tmp/proctest/proctest.go proc_pidpath+PROC_PIDVNODEPATHINFO sample ; /tmp/proctest/procargs.go KERN_PROCARGS2+PROC_PIDTBSDINFO sample ; /tmp/proctest/gopstest.go gopsutil v4 field-by-field test
- **gopsutil_darwin_source**: $GOPATH/pkg/mod/github.com/shirou/gopsutil/v4@v4.26.3/process/process_darwin.go
- **sources**: https://access.redhat.com/solutions/35925 (Linux-only myth) ; https://github.com/oshi/oshi/issues/831 ; https://lists.gnu.org/archive/html/bug-gnu-emacs/2021-05/msg01652.html ; https://mesos.apache.org/api/latest/c++/osx_8hpp_source.html (reference KERN_PROCARGS2) ; https://ss64.com/mac/ps.html ; https://gist.github.com/s4y/1173880

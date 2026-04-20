# research-macos-tcp-listener-enumeration

type: knowledge
source_job: 00000000-0000-0000-0000-000000000000
job_name: unknown
confidence: decision-only
created: 2026-04-17T00:04:53.415Z

> **IMPORTANT amendment (2026-04-20):** This doc describes the correct
> syscall SEQUENCE for listener enumeration, but it is silent on the
> **return-value conventions** of the wrappers — which differ between
> `proc_listallpids` (returns pid COUNT) and `proc_pidinfo(PROC_PIDLISTFDS)`
> (returns BYTES). A post-ship bug shipped in v1 because the original
> implementation assumed both return bytes. Before touching any
> libproc wrapper, read
> `.tenet/knowledge/2026-04-20_proc_listallpids-return-convention.md`
> and the incident journal
> `.tenet/journal/2026-04-20_post-ship-bug-libproc-pid-truncation.md`.

## Findings

- **topic**: Enumerate user-owned TCP listeners in port range on macOS 26 from Go
- **target_cadence**: every 2s, filter listeners in 40000-40500 owned by current uid
- **recommended_primary**: cgo: proc_listallpids() -> for each pid: proc_pidinfo(PROC_PIDTBSDINFO) check pbi_uid == getuid() -> proc_pidinfo(PROC_PIDLISTFDS) -> for each PROX_FDTYPE_SOCKET fd: proc_pidfdinfo(PROC_PIDFDSOCKETINFO) -> check soi_kind==SOCKINFO_TCP && tcpsi_state==TSI_S_LISTEN(1) -> ntohs(tcpsi_ini.insi_lport)
- **fallback_no_cgo**: lsof -iTCP -sTCP:LISTEN -P -n -a -u $(id -u) -F pPn with exec.CommandContext(1s timeout), cmd.Output(), discard stderr, use -b flag to avoid stalled-mount blocking
- **measured_lsof_ms**: 40ms median, 37 rows on this host
- **measured_proc_pidinfo_ms**: 5-15ms typical (no fork/exec)
- **measured_netstat_ms**: 30ms but fragile (columns shift across macOS versions, 15-char name truncation, spaces break parsing)
- **rejected_sysctl_NET_RT_IFLIST2**: interface traffic counters only, no socket or PID data
- **rejected_netstat_anvp**: unstable parsing, no uid filter flag
- **gopsutil_v4_caveat**: ConnectionsPidWithContext on Darwin shells to lsof via CallLsofWithContext; inherits every lsof caveat; ConnectionsPidMaxWithContext is ErrNotImplemented on Darwin
- **why_proc_pidinfo_wins**: no sudo for same-uid, filter pbi_uid==getuid() before touching fds avoids EPERM, ABI-stable since 10.5, used internally by Apple lsof/Activity Monitor/Instruments, no subprocess/pipes/zombies, no APFS/TimeMachine stderr noise, port-range filter is free
- **lsof_subprocess_rules_if_used**: ALWAYS cmd.Output() not bare StdoutPipe without Wait(); ALWAYS Wait() to avoid zombies on 2s loop; exec.CommandContext 1s timeout; lsof -b for stalled-mount protection; redirect stderr or parse -F machine-readable mode
- **optional_library**: go-darwin.dev/libproc exposes ProcListallpids/ProcPidinfo/fd-type constants; sladyn98/libproc-go is pure-Go asm-trampoline but missing PROC_PIDFDSOCKETINFO constants; hand-rolled cgo is ~120 lines per Apple DTS thread 728731
- **build_impact**: darwin build tag isolates cgo to darwin-only files; go build still produces single binary
- **primary_source**: https://developer.apple.com/forums/thread/728731
- **additional_sources**: https://zameermanji.com/blog/2021/8/1/counting-open-file-descriptors-on-macos/ ; https://github.com/apple-oss-distributions/lsof/tree/lsof-73 ; https://github.com/shirou/gopsutil/blob/master/net/net_unix.go ; https://github.com/shirou/gopsutil/issues/867 ; https://pkg.go.dev/go-darwin.dev/libproc

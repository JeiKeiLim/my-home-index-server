# research-macos-external-process-log-capture

type: knowledge
source_job: 00000000-0000-0000-0000-000000000000
job_name: unknown
confidence: decision-only
created: 2026-04-17T00:03:53.864Z

## Findings

- **topic**: macOS 26 external-process stdout/stderr capture feasibility
- **why_researched**: Interview Round 3 user answer requested 'capture for all processes with graceful degradation'. Need to know if 'all' is even feasible.
- **conclusion**: NOT feasible without sudo or special entitlements for processes not spawned by the dashboard. Every viable technique (log stream, pty tee, ptrace/task_for_pid, dtrace) is blocked by one of SIP / Hardened Runtime / TCC / root requirement.
- **key_findings**: {"log_stream":"Only captures os_log/OSLog output. Plain fmt.Println/console.log/print use POSIX write(2) and never touch Unified Logging System. log stream --predicate processID==X returns NOTHING for typical dev servers.","pty_tee":"Cannot retroactively tee a pty — the kernel delivers each byte once to whoever holds the master fd (iTerm2/Terminal helper). Detached processes have fd1/2 pointed at /dev/null or pipes with no second reader. reptyr-style tty-stealing is Linux-only.","ptrace_task_for_pid":"ptrace on macOS has PT_ATTACH but no PT_SYSCALL. task_for_pid requires caller signed with com.apple.security.cs.debugger AND target signed with com.apple.security.get-task-allow=true. Normal go build binaries have neither. Returns KERN_FAILURE (5).","dtrace":"Requires root. Also requires SIP booted with csrutil enable --without dtrace. syscall::write:entry unreachable without sudo.","endpoint_security":"Requires com.apple.developer.endpoint-security.client entitlement, granted by Apple individually, needs system extension. Overkill and inaccessible for a local dev tool."}
- **graceful_degradation_strategy**: Maintain a PID-to-source map of processes we ourselves spawned. For any PID lookup: (1) if in our map -> stream from ring buffer; (2) else -> UI shows 'logs unavailable — relaunch via dashboard to capture'. Never attempt attach; never block.
- **recommended_spawn_capture**: For dashboard-launched processes: use os/exec with stdout+stderr both piped into one io.Pipe. Goroutine scans lines with bufio.Scanner (1MB max token), appends to ring buffer (container/ring or mutex-guarded []string cap 1000), and publishes to an SSE broker with buffered channel (cap 256, drop-oldest on slow consumers so one stalled browser can't stall the writer). htmx client uses <div hx-ext='sse' sse-connect='/logs/{port}' sse-swap='line' hx-swap='beforeend'>. SSE handler writes rb.Snapshot() on connect then subscribes.
- **implication_for_spec**: Spec must reframe 'logs for all processes' as 'logs for dashboard-launched processes, with clear UI message for external ones'. The replay feature partially mitigates: 'click replay to relaunch via dashboard so logs work next time'.
- **sources**: ["https://developer.apple.com/library/archive/documentation/System/Conceptual/ManPages_iPhoneOS/man2/ptrace.2.html","https://developer.apple.com/documentation/security/hardened-runtime","https://github.com/MaxSchaefer/macos-log-stream","https://threedots.tech/post/live-website-updates-go-sse-htmx/","https://htmx.org/extensions/sse/"]

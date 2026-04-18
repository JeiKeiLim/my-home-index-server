# failure-job-3-trial-1

type: journal
source_job: c4b904c1-9350-417a-9a3a-fb963b03dd56
job_name: scanner (cgo libproc + lsof fallback)
created: 2026-04-17T03:19:04.950Z

## Findings

- **trial**: 1
- **job**: job-3 scanner cgo libproc + lsof fallback
- **verdict**: code_critic FAILED (9 findings); test_critic FAILED (5 findings, 4 missing tests); playwright N/A (PASSED)
- **root_cause**: Tests are asymmetric across the two scanner implementations. The dev job assumes 'libproc is the primary; lsof is fallback' so tests were weighted toward libproc. But the two are interchangeable production code paths that both must satisfy all invariants (ctx timeout, UDP filter, port-range filter, fd stability). Test names also drift from the decomposition contract (TestA9, TestA10, TestA11 are named differently).
- **blockers_to_fix**: 1) Rename / add test symbols TestA9_ScannerIgnoresUDP, TestA10_ScannerRespectsContextTimeout, TestA11_NoFDLeakOverManyScans (or add aliases alongside existing names). 2) Add TestA11 fd-leak loop against lsof (500 iters) — this is the ACTUAL anti-scenario since subprocess pipe leaks are the real risk. 3) Add TestA10 ctx-cancellation test for libproc (1us deadline or pre-cancelled ctx must error). 4) Add TestA9 UDP integration test: spawn SOCK_DGRAM on test port, assert BOTH scanners return empty. 5) Extend TestScanner_ExcludesOutOfRangePorts to loop over {libproc, lsof}. 6) lsof.go: don't treat stdout-non-empty with exit 1 as a hard error — lsof may emit valid partial output with warnings. Parse stdout even on exit-1 if stdout has p<pid> records; only fail hard on exit>=2 or empty+error. 7) Wrap libprocScanner.Scan with context.WithTimeout(ctx, lsofScanBudget) for Iron Law 6 consistency (belt-and-suspenders). 8) Drop the int32 cast on os.Getpid() comparison in libproc_darwin.go. 9) countOpenFDs helper in integration_darwin_test.go: replace with safer fd enumeration — either read /dev/fd directly (ls /dev/fd or similar) OR use lsof with -b and ctx timeout. Use /dev/fd readdir which avoids subprocess.
- **next_approach**: Retry trial 2 addressing all 9+4 findings. Keep the package structure and public interface. This is primarily hardening + symmetry.

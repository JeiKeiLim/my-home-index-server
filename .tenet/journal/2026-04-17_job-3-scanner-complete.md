# job-3-scanner-complete

type: journal
source_job: c4b904c1-9350-417a-9a3a-fb963b03dd56
job_name: scanner (cgo libproc + lsof fallback)
created: 2026-04-17T03:31:28.547Z

## Findings

- **job**: job-3 scanner cgo libproc + lsof fallback
- **verdict**: passed on retry trial 2 after failing trial 1 on asymmetric test coverage
- **deliverables**: internal/scanner/{scanner.go, libproc_darwin.go, libproc_stub.go, lsof.go, scanner_test.go, integration_darwin_test.go}. Scanner interface + Auto(cfg) picker. libproc cgo walk (proc_listallpids -> PROC_PIDTBSDINFO uid filter -> PROC_PIDLISTFDS -> PROC_PIDFDSOCKETINFO + TCP LISTEN + port-range filter, own-pid excluded). lsof path: exec.CommandContext 1s budget, argv '-iTCP -sTCP:LISTEN -P -n -a -u $(id -u) -F pPn -b', parses stdout first then classifies exit code. Both wrappers enforce ctx.WithTimeout(ctx, scanBudget) — Iron Law 6 uniformly applied.
- **test_symmetry_achieved**: TestA9_ScannerIgnoresUDP (SOCK_DGRAM, both impls), TestA10_ScannerRespectsContextTimeout (pre-cancelled + 1us, both impls), TestA11_NoFDLeakOverManyScans (500 iters each, growth<=20), TestScanner_ExcludesOutOfRangePorts loops both impls, TestScanner_ExcludesOwnPID both impls. countOpenFDs helper uses filepath.Glob('/dev/fd/*') to avoid subprocess hazards in test suite.
- **lesson**: For any module that exposes two interchangeable implementations behind one interface, every invariant test must loop over BOTH impls. Writing asymmetric tests ('implement for the primary, verify on the primary, ship') hides bugs in the fallback. Also: when a spec mandates specific test symbols, add them verbatim — don't rename them and expect graders to follow the intent.

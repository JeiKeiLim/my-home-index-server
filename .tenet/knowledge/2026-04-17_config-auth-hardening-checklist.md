# config-auth-hardening-checklist

type: knowledge
source_job: 5b7f7e07-914d-4d82-8495-7fb6e38b728d
job_name: config + auth
confidence: implemented-and-tested
created: 2026-04-17T03:03:53.988Z

## Findings

- **lesson**: Small Go library packages that look trivial (config loader + auth helpers) have 5 easy-to-miss correctness traps. Check for all five in any future hardening pass before declaring done.
- **checklist_for_similar_modules**: 1) Any in-process map keyed by untrusted input (IPs, user tokens, session IDs) MUST be bounded: cap + LRU or janitor. Unbounded + 'we'll prune on revisit' is an IP-rotation OOM. 2) Any writeback of secrets or one-time generated data MUST use atomic write: open tmp, f.Sync(), close, rename, then fsync parent dir. Bare os.WriteFile+os.Rename loses data on crash — doubly painful when the secret was printed to stdout once and cannot be regenerated. 3) Zero-value ambiguity: Go zero values for int (0) are indistinguishable from 'caller did not set this'. For 'explicit 0 means X' semantics, use *int (nil=unset, non-nil-to-0=explicit) or a negative sentinel. Document it in the godoc. 4) Dotenv files accept 'export KEY=val' syntax (godotenv does); persistence rewriters must strip/re-apply the 'export ' prefix around = splits OR duplicates accumulate. 5) Token/secret length checks: reject short inputs BY CONTRACT with an explicit len<min guard, not incidentally via hash mismatch. Put the guard on caller input (not the secret) so constant-time posture is preserved.
- **also_important_tests**: a) Banner / one-time output: assert PRINTED EXACTLY ONCE. A second load must NOT re-emit. Easy to forget; subtle but user-facing. b) Lockout magnitude: assert retryAfter > window*0.9 (not just <= window) so a buggy 1-second lockout fails. c) Lockout persistence under continued attack: recording more failures mid-window must NOT reset or extend the block surprisingly.
- **applies_to**: config+auth loader/issuer/verifier modules in any Go service that reads .env, auto-generates secrets, issues signed cookies, and rate-limits logins.

# job-5-store-complete

type: journal
source_job: 8b8e450a-2cc4-4aef-9208-c03620f7e3ba
job_name: store (labels + remembered, atomic JSON)
created: 2026-04-17T05:09:28.062Z

## Findings

- **job**: job-5 store
- **verdict**: passed on retry 2 after fixing Command type mismatch + labels-cap gap + slice aliasing
- **key_decisions**: Remembered.Command is []string (matches inspector/process contracts). LabelKey = sha256(cwd \x00 strings.Join(command, \x00))[:16] — null separator prevents ['ls -l'] vs ['ls','-l'] collisions. persistLocked evicts Remembered first, then oldest labels by UpdatedAt, then raises ErrStateSizeExceeded. ListRemembered/FindRemembered/Remember deep-copy Command+Env slices to prevent aliasing.
- **lesson**: Always cross-reference the Go interface contracts in the decomposition before implementing — informal JSON samples in spec prose are documentation, but the Go struct signatures between DAG jobs are the API. A string vs []string mismatch would have been caught at job-6 compile time but at much higher cost than 5 minutes of cross-referencing.

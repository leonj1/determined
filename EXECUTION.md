# Execution behaviour

Each iteration, in the current working directory:

1. If `STOP.md` exists → exit **0** (done).
2. If the time budget is exhausted → exit **1**. The budget is checked *between*
   iterations, so an in-flight tool run always finishes first.
3. Otherwise run the selected tool's command, streaming its output live **and**
   teeing it to `logs/iter-NNNN-<timestamp>.log`.
4. If the tool exits non-zero → abort immediately, exit **1**.
5. `SIGINT` / `SIGTERM` → stop and exit **1**.
6. If `--max-stalled-iterations` consecutive iterations (default **3**) finish
   without a newly checked step in `STEPS.md` → stop and exit **3**. Checking
   any step resets the counter; `0` disables stall detection.

## Exit codes

| Code | Meaning                                            |
|------|----------------------------------------------------|
| `0`  | The tool created `STOP.md` (execute), or wrote `PLAN.md` + `STEPS.md` (plan). |
| `1`  | Any other termination: tool failure, budget exhausted, interrupted, or a stalled plan round. |
| `2`  | Usage error (e.g. an unsupported `--tool`).        |
| `3`  | Stalled: too many consecutive iterations without a newly checked step. |

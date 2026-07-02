# Execution behaviour

Each iteration, in the current working directory:

1. If `STOP.md` exists → exit **0** (done).
2. If the time budget is exhausted → exit **1**. The budget is checked *between*
   iterations, so an in-flight tool run always finishes first.
3. Otherwise run the selected tool's coding command, streaming its output live **and**
   teeing it to `logs/iter-NNNN-<timestamp>.log`. Before each run it prints
   `Starting Step N of M`, or `Starting Step N` when the total step count cannot
   be determined; after a successful run it prints `Completed Step N`. Once at
   least one step has completed, it prints an ETA based on the average duration
   of completed steps so far.
4. If `STEPS.md` shows that the tool marked the intended task complete during
   the iteration, run the selected tool again as a verifier. The verifier reviews
   the uncommitted diff against `AGENTS.md` and the intended `STEPS.md` item,
   streaming to the terminal and `logs/iter-NNNN-verify-<timestamp>.log`.
5. If verification passes, remove stale `VERIFICATION.md` feedback, stage every
   repository change, and commit it with `CHORE: save completed task changes`. A
   clean worktree is treated as success.
6. If verification fails, restore `STEPS.md` to its pre-run task-marker state,
   remove `STOP.md`, write the verifier's reason to `VERIFICATION.md`, and retry
   the same coding step. After `--max-verification-retries` rejections for the
   same step, abort with exit **1**.
7. If the tool exits non-zero, or if committing completed-task changes fails →
   abort immediately, exit **1**.
8. `SIGINT` / `SIGTERM` → stop and exit **1**.

## Exit codes

| Code | Meaning                                            |
|------|----------------------------------------------------|
| `0`  | The tool created `STOP.md` (execute), or wrote `PLAN.md` + `STEPS.md` (plan). |
| `1`  | Any other termination: tool failure, verification failure, commit failure, budget exhausted, interrupted, or a stalled plan round. |
| `2`  | Usage error (e.g. an unsupported `--tool`).        |

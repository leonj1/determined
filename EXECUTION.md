# Execution behaviour

Each iteration, in the current working directory:

1. If `STOP.md` exists → exit **0** (done).
2. If the time budget is exhausted → exit **1**. The budget is checked *between*
   iterations, so an in-flight tool run always finishes first.
3. Otherwise run the selected tool's command, streaming its output live **and**
   teeing it to `logs/iter-NNNN-<timestamp>.log`. Before each run it prints
   `Starting Step N of M`, or `Starting Step N` when the total step count cannot
   be determined; after a successful run it prints `Completed Step N`. Once at
   least one step has completed, it prints an ETA based on the average duration
   of completed steps so far.
4. If `STEPS.md` shows that the tool marked a task complete during the
   iteration, stage every repository change and commit it with
   `CHORE: save completed task changes`. A clean worktree is treated as success.
5. If the tool exits non-zero, or if committing completed-task changes fails →
   abort immediately, exit **1**.
6. `SIGINT` / `SIGTERM` → stop and exit **1**.

## Exit codes

| Code | Meaning                                            |
|------|----------------------------------------------------|
| `0`  | The tool created `STOP.md` (execute), or wrote `PLAN.md` + `STEPS.md` (plan). |
| `1`  | Any other termination: tool failure, commit failure, budget exhausted, interrupted, or a stalled plan round. |
| `2`  | Usage error (e.g. an unsupported `--tool`).        |

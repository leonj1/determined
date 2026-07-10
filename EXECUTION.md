# Execution behaviour

Execute mode runs against a `PLAN.md` / `STEPS.md` pair in the current working
directory. `STEPS.md` must be a markdown checkbox list — one `- [ ]` item per
step, each ending with a `Done when:` line stating a checkable acceptance
condition (the format `--plan` produces). The orchestrator parses this file
itself every iteration; it never trusts the tool's own claim of completion.

## Startup

- If `PLAN.md` or `STEPS.md` is missing, the run exits **1** immediately with a
  message naming the missing file — nothing is invoked against an unplanned
  directory.
- A stale `STOP.md` sitting alongside unchecked steps is deleted with a
  warning instead of instantly ending the run as success.

## Each iteration

1. **Completion check** — if every step is checked *and* the whole-plan
   audit's `STOP.md` exists → exit **0**. A `STOP.md` that appears while
   unchecked steps remain is deleted (with a warning) and the loop continues.
2. **Budget check** — if `--max-duration` is exhausted → exit **1**. The
   budget is checked *between* iterations, so an in-flight tool run always
   finishes first.
3. **Prompt construction** — the orchestrator re-reads `STEPS.md` and aims the
   tool at exactly the next unchecked step, injecting the step number and
   text, its `Done when:` criterion, and the `NOTES.md` read/append
   instructions (see below). Every prompt opens with a short protocol
   preamble (fresh context, file roles). The worker is told to read
   `FIXES.md` first in case the step was reopened, to consult `PLAN.md` when
   the step is unclear, to check the box only after running the acceptance
   check itself, and never to touch other boxes or create `STOP.md`. When
   every box is checked, the iteration is the whole-plan audit instead. If
   `STEPS.md` contains no parseable checkboxes, the tool is asked to re-read
   `PLAN.md` and either restore a checkbox-format step list or confirm
   completion.
4. **Invocation** — the tool's command runs, streaming its output live **and**
   teeing it to `logs/iter-NNNN-<timestamp>.log`. Each invocation is bounded
   by `--max-iteration-duration` (default **15m**, `0` = unlimited); a timed
   out invocation counts as a failed invocation, not an interruption.
5. **Failure handling** — a non-zero tool exit retries the same iteration
   until `--max-consecutive-failures` (default **3**) failures occur in a row;
   any success resets the count. Only when the cap is hit does the run abort
   with exit **1**. `SIGINT` / `SIGTERM` stop immediately with exit **1**.
6. **Verification pass** (`--verify`, default **on**) — for every step this
   iteration newly checked, a fresh reviewer invocation confirms the
   acceptance criterion actually holds. The reviewer is prompted skeptically
   (assume incomplete until the check is seen to pass) and is forbidden from
   fixing anything itself or touching other steps. If the criterion does not
   hold, the reviewer unchecks the step in `STEPS.md` and appends a
   `## Step N` entry to `FIXES.md` naming the failing check, so the loop
   re-runs the step with that feedback.
7. **Git checkpoint** (`--git-checkpoint`, default **on**) — each newly
   checked step that survived verification is committed as
   `determined: step N: <step text>` (`git add -A && git commit`). Outside a
   git repository the checkpoint is skipped with a terminal note; a failed git
   command is noted and ignored.
8. **Stall detection** — if `--max-stalled-iterations` consecutive iterations
   (default **3**) finish without a newly checked step → exit **3**. Checking
   any step resets the counter; a verifier or audit rejection counts as no
   progress, which bounds worker/reviewer ping-pong. `0` disables stall
   detection.

## The whole-plan audit

Checked boxes alone are not success. Once every step is checked, one more
invocation reads `PLAN.md` and `STEPS.md` and audits whether the
implementation genuinely satisfies the plan. The audit must run the project's
build and test suite — not just read — and judge the steps as a whole, since
it is the only invocation positioned to catch integration failures between
individually verified steps:

- If a step is not actually satisfied, the audit unchecks it and appends the
  reason to `FIXES.md` — the loop resumes on that step. It never fixes
  anything itself.
- If everything is satisfied, the audit creates `STOP.md` containing a short
  report: what was built, what checks ran, and their results.

Only *all steps checked + `STOP.md` present* ends the run with exit **0**.

## Protocol files

| File       | Role                                                              |
|------------|-------------------------------------------------------------------|
| `PLAN.md`  | The plan the steps implement; read by the audit. Required at startup. |
| `STEPS.md` | Checkbox step list with `Done when:` criteria; the loop's source of truth. Required at startup. |
| `STOP.md`  | Created by the whole-plan audit to approve the finished run, holding a short audit report; deleted if it appears early. |
| `NOTES.md` | Cross-iteration memory (see below); created by the tool on first use. |
| `FIXES.md` | Why the verifier or audit reopened a step; appended by reviewer invocations, read back by the worker when it re-runs a reopened step. |

## NOTES.md

Each iteration runs in a fresh tool invocation with no memory of earlier ones,
so the injected prompt tells the tool to read `NOTES.md` (if it exists) before
starting, and to append any decisions, conventions, or gotchas later steps need
to know before finishing. The file lives in the working directory alongside
`PLAN.md` and `STEPS.md` and is created by the tool itself on first use.

## Exit codes

| Code | Meaning                                            |
|------|----------------------------------------------------|
| `0`  | All steps checked and the audit created `STOP.md` (execute), or `PLAN.md` + `STEPS.md` written (plan). |
| `1`  | Any other termination: too many consecutive tool failures, budget exhausted, interrupted, missing `PLAN.md`/`STEPS.md` at startup, or a stalled plan round. |
| `2`  | Usage error (e.g. an unsupported `--tool`).        |
| `3`  | Stalled: too many consecutive iterations without a newly checked step. |

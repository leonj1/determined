# Execution behaviour

Execute mode runs against a `PLAN.md` / `STEPS.md` pair in the current working
directory. `STEPS.md` must be a markdown checkbox list — one `- [ ]` item per
step, each ending with a `Done when:` line stating a checkable acceptance
condition (the format `--plan` produces). The orchestrator parses this file
itself every iteration; it never trusts the tool's own claim of completion,
and a tamper guard reverts any worker edit to it beyond checking the step the
worker was given (see below).

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
6. **Tamper guard** (always on) — after a single-step work invocation, the
   only `STEPS.md` change accepted is that step's own box flipping
   `[ ]` → `[x]`. If the parsed steps differ from the pre-iteration snapshot
   in any other way — reworded step text, an altered `Done when:` criterion,
   steps added, deleted, or reordered, any other box flipped — the file is
   restored byte-for-byte from the snapshot with a warning naming the
   violation, re-applying the target step's check if the edit also made it
   legitimately. A surviving check still passes through the gate, verifier,
   and checkpoint below; a full revert counts as no progress. The whole-plan
   audit and the no-parseable-steps rewrite are exempt — they legitimately
   uncheck or rewrite the file.
7. **Check command gate** (`--check-cmd`, default **off**) — when set, an
   iteration that newly checked a step runs the command once via `sh -c`,
   bounded by a 10-minute timeout, streaming its output live; one success
   covers every step checked this iteration. On failure the newly checked
   steps are mechanically unchecked, a `## Step N` entry naming the command
   and the tail of its output (last 4000 bytes) is appended to `FIXES.md`,
   and the verification pass and git checkpoint are skipped — the loop simply
   re-runs the steps. A check failure is a verdict on the work, not a tool
   failure: it never counts toward `--max-consecutive-failures`.
8. **Verification pass** (`--verify`, default **on**) — for every step this
   iteration newly checked, a fresh reviewer invocation confirms the
   acceptance criterion actually holds. The reviewer is prompted skeptically
   (assume incomplete until the check is seen to pass) and is forbidden from
   fixing anything itself or touching other steps. If the criterion does not
   hold, the reviewer unchecks the step in `STEPS.md` and appends a
   `## Step N` entry to `FIXES.md` naming the failing check, so the loop
   re-runs the step with that feedback.
9. **Git checkpoint** (`--git-checkpoint`, default **on**) — each newly
   checked step that survived verification is committed as
   `determined: step N: <step text>` (`git add -A && git commit`). Outside a
   git repository the checkpoint is skipped with a terminal note; a failed git
   command is noted and ignored.
10. **Stall detection** — if `--max-stalled-iterations` consecutive iterations
    (default **3**) finish without a newly checked step, the run first tries a
    replan of the stuck step (see below); only when no replan is available or
    the replan is ineffective does it exit **3**. Checking any step resets the
    counter; a tamper-guard revert or a check-gate, verifier, or audit
    rejection counts as no progress, which bounds worker/reviewer ping-pong.
    `0` disables stall detection.

## Replanning a stuck step

A step that repeatedly fails verification is usually too large for one
invocation, not impossible — so hitting the stall cap escalates to a planning
move before giving up. One invocation is aimed at exactly the stuck step: read
`PLAN.md` (plus `FIXES.md` and `NOTES.md`) and replace step N in `STEPS.md`
with 2–4 smaller checkbox steps with checkable `Done when:` criteria, keeping
every other step exactly as it is, checking nothing and implementing nothing.

The result is judged by its effect on the file, never the tool's word:

- **Success** — the stuck step demonstrably changed (its text or criterion
  differs, or the step count did) and every previously completed step kept its
  check. The stall counter resets and the loop resumes on the new steps.
- **Failure** — the file is unchanged, nothing parses, or finished work lost
  its check (the file is then restored from the pre-replan snapshot). The run
  exits **3** as it would have without replanning.

`--max-replans` (default **1**, `0` disables) bounds the escalation; each
attempt consumes one regardless of outcome. Replanning never applies when
every box is already checked (audit ping-pong) — there is no single stuck
step to split.

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
| `STEPS.md` | Checkbox step list with `Done when:` criteria; the loop's source of truth. Required at startup. A work invocation may only check its own step's box — the tamper guard reverts any other edit to the parsed steps. |
| `STOP.md`  | Created by the whole-plan audit to approve the finished run, holding a short audit report; deleted if it appears early. |
| `NOTES.md` | Cross-iteration memory (see below); created by the tool on first use. |
| `FIXES.md` | Why the check gate, verifier, or audit reopened a step; appended by reviewer invocations (and mechanically by a failed `--check-cmd`), read back by the worker when it re-runs a reopened step. |

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
| `3`  | Stalled: too many consecutive iterations without a newly checked step, after any `--max-replans` budget was spent or the replan proved ineffective. |

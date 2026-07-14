# Execution behaviour

Execute mode (`./determined -exec`) runs against a `PLAN.md` / `STEPS.md` pair
in the current working directory. Combined with `--plan`, it starts as soon as
planning succeeds; invoking `determined` with neither flag prints the usage
screen instead. `STEPS.md` must be a markdown checkbox list — one `- [ ]` item per
step, each ending with a `Done when:` line stating a checkable acceptance
condition (the format `--plan` produces). The orchestrator parses this file
itself every iteration; it never trusts the tool's own claim of completion.

## Startup

- If `PLAN.md` or `STEPS.md` is missing, the run exits **1** immediately with a
  message naming the missing file — nothing is invoked against an unplanned
  directory.
- A stale `STOP.md` sitting alongside unchecked steps is deleted with a
  warning instead of instantly ending the run as success.
- With specialist reviews enabled, an existing `STOP.md` alongside completed
  steps is also removed so a previous run cannot bypass the current review
  sequence.

## Each iteration

1. **Completion check** — if every step is checked *and* the whole-plan
   audit's `STOP.md` exists → exit **0**. A `STOP.md` that appears while
   unchecked steps remain is deleted (with a warning) and the loop continues.
2. **Budget check** — if `--max-duration` / `-t` is exhausted → exit **1**. The
   budget is checked *between* iterations, so an in-flight tool run always
   finishes first.
3. **Prompt construction** — the orchestrator re-reads `STEPS.md` and aims the
   tool at exactly the next unchecked step, injecting the step text and its
   `Done when:` criterion, plus the `NOTES.md` read/append instructions (see
   below). When every box is checked, the iteration runs the specialist review
   sequence and then the whole-plan audit. If `STEPS.md` contains no parseable
   checkboxes, the tool is asked to restore a checkbox-format step list or
   confirm completion.
4. **Invocation** — the tool's command runs, streaming its output live **and**
   teeing it to `logs/iter-NNNN-<timestamp>.log`. Each invocation is bounded
   by `--max-iteration-duration` (default **15m**, `0` = unlimited); a timed
   out invocation counts as a failed invocation, not an interruption.
   Each stage starts with a brief timestamped status such as
   `==> [2026-07-11 09:30:00] executing step 2`.
5. **Failure handling** — a non-zero tool exit retries the same iteration
   until `--max-consecutive-failures` (default **3**) failures occur in a row;
   any success resets the count. Only when the cap is hit does the run abort
   with exit **1**. `SIGINT` / `SIGTERM` stop immediately with exit **1**.
6. **Verification pass** (`--verify`, default **on**) — for every step this
   iteration newly checked, a fresh reviewer invocation confirms the
   acceptance criterion actually holds. If it does not, the reviewer unchecks
   the step in `STEPS.md` and appends what is wrong to `FIXES.md`, so the loop
   re-runs it.
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

## Specialist reviews and the whole-plan audit

Checked boxes alone are not success. Once every step is checked,
`--specialized-reviews` (default **on**) runs three fresh, independent review
invocations in order:

1. **Security** — authentication/authorization, trust boundaries, injection,
   validation, secrets, cryptography, dependencies, and sensitive data.
2. **Performance** — complexity, unbounded work, I/O, allocations,
   concurrency, resource leaks, and relevant benchmark/profile evidence.
3. **Reliability and maintainability** — errors, races, cleanup, edge cases,
   tests, compatibility, readability, coupling, and project conventions.

A reviewer reports only concrete, actionable findings. It appends the finding
and evidence to `FIXES.md`, then reopens the relevant checkbox or adds an
unchecked remediation step with a `Done when:` criterion. That immediately
blocks later gates; after remediation, all specialist reviews run again.
`--specialized-reviews=false` skips these three gates.

After all enabled specialist reviews approve, one final invocation reads
`PLAN.md` and `STEPS.md` and audits whether the implementation genuinely
satisfies the plan:

- If a step is not actually satisfied, the audit unchecks it and appends the
  reason to `FIXES.md` — the loop resumes on that step.
- If `CRITERIA.md` exists (written by a `--criteria` session), the audit also
  requires each of its BDD journey tests to exist as an automated test and
  pass; a missing or failing test adds a new unchecked remediation step and a
  `FIXES.md` entry.
- If everything is satisfied, the audit creates `STOP.md`.

Only *all steps checked + `STOP.md` present* ends the run with exit **0**.

## Protocol files

| File       | Role                                                              |
|------------|-------------------------------------------------------------------|
| `PLAN.md`  | The plan the steps implement; read by the audit. Required at startup. |
| `STEPS.md` | Checkbox step list with `Done when:` criteria; the loop's source of truth. Required at startup. |
| `STOP.md`  | Created by the whole-plan audit to approve the finished run; deleted if it appears early. |
| `NOTES.md` | Cross-iteration memory (see below); created by the tool on first use. |
| `FIXES.md` | Why a verifier, specialist, or audit reopened/added a step; appended by reviewer invocations. |
| `CRITERIA.md` | Optional user-approved BDD journey tests from a `--criteria` session; enforced by the final audit. |

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
| `2`  | Usage error (e.g. an unsupported `--tool` or `--model` with `pi`). |
| `3`  | Stalled: too many consecutive iterations without a newly checked step. |

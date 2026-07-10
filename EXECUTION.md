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
- A `run-report.json` or `STALLED.md` left by a previous run is removed, so a
  fresh run never sits alongside reports describing an older one; every
  termination — even the missing-files exit above — writes a fresh
  `run-report.json` (see below), and only a stalled one writes `STALLED.md`.

## Each iteration

1. **Completion check** — if every step is checked *and* the whole-plan
   audit's `STOP.md` exists with validated evidence (see the whole-plan
   audit below) → exit **0**. A `STOP.md` that appears while unchecked steps
   remain is deleted (with a warning) and the loop continues.
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
   check itself, and never to touch other boxes or create `STOP.md`. When the
   step's criterion quotes a command in backticks, the prompt also warns that
   the orchestrator will re-run that exact command itself to verify the step. When
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
8. **Verification pass** — for every step this iteration newly checked, the
   step's `Done when:` criterion is verified, in one of two tiers:
   - **Mechanical done-when check** — when the criterion contains exactly one
     non-empty backtick span (`` `cmd ...` ``), that span is the step's check
     and the orchestrator runs it itself via `sh -c`, streamed live and
     bounded by the same 10-minute timeout as the check gate. Exit 0 verifies
     the step — no AI reviewer runs for it — and it proceeds to the git
     checkpoint as a verified step. A non-zero exit or timeout mechanically
     unchecks the step and appends a `## Step N` entry to `FIXES.md` naming
     the command and the tail of its output (last 4000 bytes), mirroring the
     check-gate entry, and the rejection is recorded as
     `done-when check failed: <cmd>` — so `STALLED.md`, failed-attempt
     stashing, and `run-report.json` all see it. Like the check gate, a
     failed criterion is a verdict on the work, never a tool failure: it does
     not count toward `--max-consecutive-failures`. The parsing rule is
     deliberately conservative: no backticks, more than one span, an unpaired
     backtick, or an empty span all mean no command, and the step falls to
     the reviewer tier below. The mechanical check runs regardless of
     `--verify`.
   - **AI reviewer fallback** (`--verify`, default **on**) — every other
     (prose-criterion) step gets a fresh reviewer invocation confirming the
     acceptance criterion actually holds. The reviewer is prompted skeptically
     (assume incomplete until the check is seen to pass) and is forbidden from
     fixing anything itself or touching other steps. If the criterion does not
     hold, the reviewer unchecks the step in `STEPS.md` and appends a
     `## Step N` entry to `FIXES.md` naming the failing check, so the loop
     re-runs the step with that feedback. `--verify=false` disables only this
     fallback, not the mechanical check.
9. **Git checkpoint** (`--git-checkpoint`, default **on**) — each newly
   checked step that survived verification is committed as
   `determined: step N: <step text>` (`git add -A && git commit`). Outside a
   git repository the checkpoint is skipped with a terminal note; a failed git
   command is noted and ignored.
10. **Failed-attempt stashing** (`--stash-attempts`, default **on**) — the
    orchestrator counts, per step, how many times the check gate, done-when
    check, verifier, or audit rejected a checked box. The first rejection retries in place — the
    attempt may be one small fix away — but from the second rejection of the
    same step onward, the failed attempt is `git stash`ed so the retry starts
    from the last verified checkpoint instead of on top of work that keeps
    failing (see below).
11. **Stall detection** — if `--max-stalled-iterations` consecutive iterations
    (default **3**) finish without a newly checked step, the run first tries a
    replan of the stuck step (see below); only when no replan is available or
    the replan is ineffective does it exit **3**, writing the `STALLED.md`
    handoff report (see below) on the way out. Checking any step resets the
    counter; a tamper-guard revert or a check-gate, done-when-check, verifier,
    or audit rejection counts as no progress, which bounds worker/reviewer
    ping-pong.
    `0` disables stall detection.

## Stashing a rejected attempt

A rejected attempt normally stays in the working tree, so the retry builds on
possibly broken changes. Once the same step has been rejected twice, the
orchestrator concludes the attempt's foundation is suspect and stashes it —
mechanically, like the check gate, never by asking the tool:

- `git stash push --include-untracked` captures everything the attempt
  changed since the last checkpoint, **excluding** the protocol files
  (`PLAN.md`, `STEPS.md`, `STOP.md`, `NOTES.md`, `FIXES.md`, the planning
  files) and the log directory, so the rejection record the retry needs to
  read survives the stash.
- The attempt is preserved as evidence, not discarded: a mechanical
  `## Step N` entry in `FIXES.md` records the stash's immutable commit hash
  (`stash@{N}` positions rot as new stashes push in) and a diffstat of what it
  changed, and the re-run prompt points the worker at the stash — inspect it,
  reuse ideas from it, but never apply it wholesale.
- Once the step finally passes verification and is checkpointed, its stashes
  are dropped: they are dead weight in the repository's stash stack from then
  on.

Stashing assumes every uncommitted change is the run's own work, so it is
enabled only when `--git-checkpoint` is on, the directory is a git repository,
and `git status` finds the tree clean at startup (protocol files aside). A
tree carrying the user's own uncommitted changes disables stashing for the
whole run with a warning — pre-existing work is never stashed — and rejected
steps then retry in place exactly as without the feature. Rejection counts
are keyed by the step's text and criterion, not its position, so they survive
a replan reshaping other steps; steps the audit reopens count toward the same
totals but trigger no stash, since their work was already committed.

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

## The stall handoff (STALLED.md)

A stalled exit hands the run to a human, so alongside the machine-readable
`run-report.json` a stalled run writes `STALLED.md` — a human-readable handoff
report built mechanically from data the orchestrator already tracks, with no
extra AI invocation:

```markdown
# Run stalled at step 4

step: "Add retry logic to the fetcher."
done when: TestFetchRetry passes.
rejections: 3 (full entries in FIXES.md)
  1. verifier rejected
  2. check command failed: go test ./...
  3. verifier rejected
stashed attempts:
  a1b2c3d  4 files changed, 120 insertions(+), 30 deletions(-)
iterations: 9
wall time: 42m of 1h budget
replans used: 1 of 1
```

The stuck step is the first unchecked one, quoted with its `Done when:`
criterion. Each rejection line is the short reason recorded when the check
gate, done-when check, verifier, or audit rejected the step (`FIXES.md` keeps
the full entries); the stashed attempts list the immutable stash hashes and diffstat
summaries the stash-attempts feature recorded, ready for `git stash show -p`.
When the stall is audit ping-pong (every box checked, the audit never
decides) or `STEPS.md` holds nothing parseable, the heading says so instead
of naming a step. The replans line appears only when replanning is enabled.

`STALLED.md` is written only on the stalled outcome: a stale one is removed
at startup, any one left mid-run is removed on a non-stall exit, and attempt
stashes exclude it like the other protocol files. Like the run report, it is
an observer — a write failure is warned and ignored, never changing the
outcome.

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

The audit's approval is not taken on its word: `STOP.md` must carry an
**evidence block** — a fenced code block with the `evidence` info string
listing, one per line, the project's real build/test commands the audit
actually ran:

    ```evidence
    go build ./...
    go test ./...
    ```

The lines are just the commands, no exit codes — the orchestrator re-runs
every listed command itself (via `sh -c`, streamed live, each bounded by the
same 10-minute timeout as the other gates) before accepting the approval:

- **Missing or empty block** — `STOP.md` is rejected: deleted with a warning,
  and the audit re-runs next iteration with a corrective note in its prompt
  naming the omission. The rejection is a verdict on the audit's output, not
  a tool failure, so it never counts toward `--max-consecutive-failures`; it
  does count as a no-progress iteration for stall detection, bounding audit
  ping-pong exactly like a verifier rejection.
- **A listed command fails or times out** — `STOP.md` is deleted, a
  `## Audit` entry naming the command and the tail of its output (last 4000
  bytes) is appended to `FIXES.md` (mirroring the check-gate entry), and the
  rejection is recorded as `audit evidence failed: <cmd>` so `STALLED.md` and
  `run-report.json` see it. The loop resumes: the audit should uncheck the
  step that broke or produce honest evidence, never fix anything itself.
  Again a verdict, never a tool failure.
- **Every command exits 0** — the approval stands and the run ends **0**.

Only *all steps checked + a validated `STOP.md`* ends the run with exit
**0** — even a run that starts with every box already checked and a
`STOP.md` in place validates the evidence before exiting, so success never
bypasses the check.

## The run report

Success and failure report symmetrically: on **every** termination of the
execute loop — success, stall, tool-failure abort, exhausted budget,
interruption, even missing `PLAN.md`/`STEPS.md` at startup — the orchestrator
writes a machine-readable `run-report.json` to the working directory, built
from data it already tracks. Plan mode and usage errors (exit **2**, which
happen before the loop runs) write no report.

```json
{
  "outcome": "stalled",
  "exit": 3,
  "steps": {"total": 8, "checked": 3},
  "stuck_step": 4,
  "rejections": {"4": 3},
  "report": "STALLED.md",
  "replans_used": 1,
  "iterations": 9,
  "wall_seconds": 2520,
  "log_dir": "logs"
}
```

| Field | Meaning |
|-------|---------|
| `outcome` | `"success"` (all steps checked and the audit approved), `"stalled"` (no-progress stop), or `"failed"` (every other termination: tool failures, budget, interruption, missing files) — the same grouping the exit codes use. |
| `exit` | The process exit code the run returns. |
| `steps` | How many steps the final `STEPS.md` holds (`total`) and how many are checked (`checked`); omitted when nothing parses. |
| `stuck_step` | The 1-based step a stalled run could not get past; present only when stalled. |
| `rejections` | Per-step counts of check-gate, done-when-check, verifier, and audit rejections, keyed by the step's current number (or by its text, when a replan removed the step from the file); `STOP.md` evidence rejections appear under the `audit` key. Omitted when nothing was rejected. |
| `report` | Names the human-readable `STALLED.md` handoff (see above); present only when the run stalled and the handoff was actually written. |
| `replans_used` | Replan invocations spent on stuck steps; omitted when zero. |
| `iterations` | Total tool invocations of the run: work, verify, audit, and replan alike. |
| `wall_seconds` | The run's wall-clock duration. |
| `log_dir` | Where the per-iteration logs were written. |

Fields that do not apply to a run are omitted rather than written empty.
Writing the report never changes the run's outcome: a write failure is warned
to the terminal and ignored.

## The exit notification (--notify-cmd)

Nothing else tells the user an unattended run finished. `--notify-cmd`
(default **off**) runs a shell command once via `sh -c` when the execute run
ends — on **every** termination: success, stall, tool-failure abort,
exhausted budget, interruption, even missing protocol files — after
`run-report.json` (and, on a stall, `STALLED.md`) is written. The run's
terminal state is exported to the command as environment variables, appended
to the inherited environment:

| Variable | Value |
|----------|-------|
| `DET_OUTCOME` | `success`, `stalled`, or `failed` — the same mapping `run-report.json` uses. |
| `DET_EXIT` | The process exit code the run returns (`0`, `1`, or `3`). |
| `DET_STEP` | The 1-based stuck step, mirroring the report's `stuck_step`; set only when a stalled run has one to name, unset otherwise. |
| `DET_WALL` | The run's wall time in the same human format `STALLED.md` uses (e.g. `42m`). |
| `DET_DIR` | The working directory's absolute path. |

The command's output streams to the terminal. It is deliberately not bound to
the run's own context: an interrupted (`SIGINT`/`SIGTERM`) run still
notifies, on a fresh context bounded by a 1-minute timeout. Like the reports,
the hook is an observer — a failing or timed-out command is warned to the
terminal and ignored, never changing the run's exit code. Plan mode never
runs it.

## Protocol files

| File       | Role                                                              |
|------------|-------------------------------------------------------------------|
| `PLAN.md`  | The plan the steps implement; read by the audit. Required at startup. |
| `STEPS.md` | Checkbox step list with `Done when:` criteria; the loop's source of truth. Required at startup. A work invocation may only check its own step's box — the tamper guard reverts any other edit to the parsed steps. |
| `STOP.md`  | Created by the whole-plan audit to approve the finished run, holding a short audit report plus an `evidence` fenced block naming the build/test commands the audit ran; the orchestrator re-runs those commands and accepts the file only when they all pass (see above). Deleted if it appears early or fails validation. |
| `NOTES.md` | Cross-iteration memory (see below); created by the tool on first use. |
| `FIXES.md` | Why the check gate, done-when check, verifier, or audit reopened a step; appended by reviewer invocations (and mechanically by a failed `--check-cmd`, a failed done-when check, or a stashed attempt), read back by the worker when it re-runs a reopened step. |
| `run-report.json` | Machine-readable summary written by the orchestrator on every termination of the execute loop (see above); a stale one is removed at startup. |
| `STALLED.md` | Human-readable stall handoff written by the orchestrator only on a stalled exit (see above): the stuck step, its rejection reasons, stashed attempts, and the run's counters. A stale one is removed at startup; a non-stall exit leaves none behind. |

## NOTES.md

Each iteration runs in a fresh tool invocation with no memory of earlier ones,
so the injected prompt tells the tool to read `NOTES.md` (if it exists) before
starting, and to append any decisions, conventions, or gotchas later steps need
to know before finishing. The file lives in the working directory alongside
`PLAN.md` and `STEPS.md` and is created by the tool itself on first use.

## Exit codes

| Code | Meaning                                            |
|------|----------------------------------------------------|
| `0`  | All steps checked and the audit created a `STOP.md` whose evidence validated (execute), or `PLAN.md` + `STEPS.md` written (plan). |
| `1`  | Any other termination: too many consecutive tool failures, budget exhausted, interrupted, missing `PLAN.md`/`STEPS.md` at startup, or a stalled plan round. |
| `2`  | Usage error (e.g. an unsupported `--tool`).        |
| `3`  | Stalled: too many consecutive iterations without a newly checked step, after any `--max-replans` budget was spent or the replan proved ineffective. |

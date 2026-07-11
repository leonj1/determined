# determined

**Run an AI coding CLI in a loop until the plan is actually done — verified, not taken on its word.**

You give `determined` a plan (`PLAN.md` + a `STEPS.md` checkbox list). Each
iteration it invokes your AI tool of choice (`droid`, `claude`, or `pi`) with
exactly the next unchecked step and its acceptance criterion. When the tool
checks a step off, an independent reviewer invocation verifies the criterion
actually holds — unchecking it and recording why in `FIXES.md` when it
doesn't. Every verified step is git-committed. Only a final whole-plan audit
can end the run successfully, and it is preceded by independent security,
performance, and reliability/maintainability reviews.

Don't have a plan yet? `determined --plan "your goal"` interviews you first
and writes `PLAN.md` / `STEPS.md` for you.

It grew out of hardening this bash loop, which trusts the tool completely:

```bash
while [ ! -e STOP.md ]; do
  droid exec "Read PLAN.md and STEPS.md. Implement the first incomplete step. ..."
done
```

`determined` only **orchestrates** — the AI tool still does all the work. But
unlike the one-liner, it parses progress itself, verifies every step, bounds
stalls/failures/wall-clock time, and deletes a premature `STOP.md`.

## Getting started

**1. Install** — build from source, or grab a
[release binary](https://github.com/leonj1/determined/releases)
(Linux amd64/arm64, macOS arm64):

```bash
go build -o determined ./cmd/determined
# later, keep a release binary current with:
./determined update
```

Optionally install the personal Claude and agent conventions (existing files
are overwritten):

```bash
./determined -init
```

**2. Plan** (skip if you already have `PLAN.md` / `STEPS.md`) — an *attended*
loop that asks clarifying questions, then writes the plan files:

```bash
./determined --plan "build a todo CLI"   # from a one-line goal
./determined --plan TODO.md              # or seed from a longer file
./determined --plan "test a todo UI" -prototype # minimal experimental plan
```

**3. Execute** — the *unattended* loop, run in the directory containing the
plan (ideally a clean git checkout, since the tool edits files freely):

```bash
./determined                        # droid by default, 1h budget
./determined --tool claude -t 2h    # different tool, bigger budget
```

**4. Watch it work** — per-iteration logs land in `logs/`, each verified step
becomes a git commit, and the run ends with an exit code: `0` success (audit
approved), `1` failure/budget/interrupt, `2` usage error, `3` stalled (see
[EXECUTION.md](EXECUTION.md)).

## What each mode does

### Planning mode (`--plan`, attended)

1. **Capture the goal** — write the supplied goal text (or the contents of the
   supplied file) to `GOAL.md`.
2. **Invoke the planner** — ask the selected AI tool to read the goal and
   either create clarifying questions or produce the plan files.
3. **Run the interview** — if the tool writes `QUESTIONS.md`, ask each question
   on the terminal, append the responses to `ANSWERS.md`, clear the questions,
   and invoke the planner again. Repeat until it has enough information.
4. **Create the plan** — the planner writes `PLAN.md` and a machine-checkable
   `STEPS.md` whose checkbox steps each have a concrete `Done when:` criterion.
5. **Apply the quality gate** — independently assess completeness, the
   task-specific template, ordering, step size, and acceptance criteria. Write
   issues to `REFINEMENTS.md`, refine the plan, and reassess for up to
   `--max-step-passes` rounds (skipped in prototype mode).
6. **Finish planning** — leave `PLAN.md` and `STEPS.md` in place and exit `0`,
   ready for `./determined` to execute them.

See [PLANNING.md](PLANNING.md) for details.

### Execution mode (default, unattended)

1. **Validate the inputs** — require `PLAN.md` and `STEPS.md`; delete a stale
   `STOP.md` when it cannot prove that the current run is complete.
2. **Check completion and budget** — exit `0` only when every step is checked
   and the final audit created `STOP.md`; exit `1` if `--max-duration` / `-t`
   is exhausted.
3. **Select the next step** — re-parse `STEPS.md`, choose exactly the next
   unchecked checkbox, and build a prompt containing its `Done when:`
   criterion plus the `NOTES.md` memory instructions.
4. **Invoke the tool** — stream output live and tee it to `logs/`. Kill an
   invocation after `--max-iteration-duration`, retry failures, and exit `1`
   after `--max-consecutive-failures` consecutive failures.
5. **Verify completed work** — for every newly checked step, use a fresh
   reviewer invocation to test its acceptance criterion. A failed verification
   unchecks the step and records the reason in `FIXES.md`.
6. **Checkpoint verified work** — git-commit each newly checked step that
   survives verification.
7. **Detect stalls** — exit `3` after `--max-stalled-iterations` consecutive
   iterations without a newly checked step; otherwise return to step 2.
8. **Run specialist reviews** — once all boxes are checked, independently
   review security, performance, and reliability/maintainability. A concrete
   finding is written to `FIXES.md` and reopens a relevant step (or adds a
   remediation step), returning execution to step 2.
9. **Audit the whole plan** — after the specialist gates pass, compare the
   implementation with `PLAN.md`. Reopen unsatisfied steps and record why in
   `FIXES.md`, or create `STOP.md` when the entire plan is satisfied.
10. **Finish execution** — return to the completion check, which exits `0`
    only when all boxes remain checked and `STOP.md` is present.

See [EXECUTION.md](EXECUTION.md) for details.

## Supported tools

Pick the AI coding CLI with `--tool`. Each iteration runs the tool's own
command form with the prompt built for that iteration:

| `--tool`          | Command run each iteration                                             |
|-------------------|------------------------------------------------------------------------|
| `droid` (default) | `droid exec "<prompt>" --auto high [--model <model>]`                  |
| `pi`              | `pi -p "<prompt>"`                                                     |
| `claude`          | `claude -p "<prompt>" --permission-mode acceptEdits [--model <model>]` |

`--model` is optional and only applies to `droid` (Factory model ID, e.g.
`claude-opus-4-7`) and `claude` (alias like `opus` or a full model name);
`--tool pi --model ...` exits as a usage error.

Both `droid --auto high` and `claude --permission-mode acceptEdits` exist for
the same reason: the loop is unattended, so a permission prompt would stall
iteration 1. `acceptEdits` auto-approves file edits only — not arbitrary shell
commands (that would be `bypassPermissions`, which we deliberately avoid).
Either way, run `determined` in a directory you are prepared to have edited —
ideally a clean git checkout, so every change is reviewable and revertible.

## Flags

| Flag             | Default  | Purpose                                                        |
|------------------|----------|----------------------------------------------------------------|
| `--tool`         | `droid`  | AI coding CLI to run (`droid`/`pi`/`claude`).                   |
| `--model`        | —        | Optional model ID or alias for `droid` or `claude`; rejected with `pi`. |
| `--plan`         | —        | Goal text or a file path to plan interactively; produces `PLAN.md` + `STEPS.md` instead of running the execute loop. |
| `--review-plan`  | `false`  | Critique existing `PLAN.md` + `STEPS.md`, interview the user about consequential choices, and revise without executing. |
| `--mvp`          | `false`  | Use a reduced quality gate for the smallest usable outcome. Requires `--plan`; incompatible with `--prototype`. |
| `--prototype`    | `false`  | Ask only blocking questions and skip quality refinement for fast experiments. Requires `--plan`; incompatible with `--mvp`. |
| `--max-step-passes` | `5`   | Max quality assess/refine rounds during planning or review. `0` disables refinement. |
| `--max-duration`, `-t` | `1h` | Wall-clock budget, checked between iterations. `0` = unlimited. |
| `--max-iteration-duration` | `15m` | Kill a single tool invocation after this long; the timeout counts as a failed invocation. `0` = unlimited. |
| `--max-consecutive-failures` | `3` | Abort after this many consecutive failed tool invocations; any success resets the count. |
| `--max-stalled-iterations` | `3` | Stop (exit `3`) after this many consecutive iterations check no new step. `0` disables stall detection. |
| `--verify`       | `true`   | After each newly checked step, run an independent verifier invocation that unchecks it (recording why in `FIXES.md`) if its acceptance criterion is not met. |
| `--specialized-reviews` | `true` | Before the final audit, run independent security, performance, and reliability/maintainability review gates. |
| `--git-checkpoint` | `true` | Git-commit the working tree after each verified step when running in a git repository. |
| `--log-dir`      | `logs`   | Directory for per-iteration log files.                          |
| `--version`      | —        | Print the binary's semantic version and exit.                  |
| `-init`          | `false`  | Download the personal knowledge `CLAUDE.md` to `~/.claude/CLAUDE.md` and `AGENTS.md` to `~/AGENTS.md`, overwriting existing files. |

## Commands

| Command             | Purpose                                                        |
|---------------------|----------------------------------------------------------------|
| `determined update` | Download the latest supported GitHub Release binary and replace the current executable. |

The protocol filenames (`PLAN.md` / `STEPS.md` / `STOP.md` / `NOTES.md` /
`FIXES.md`) are hardcoded; the prompt is rebuilt each iteration from the next
unchecked step in `STEPS.md`.

## Going deeper

- [BUILD.md](BUILD.md) — build commands, runtime flags in action, and the
  versioned release build behind `determined update`.
- [PLANNING.md](PLANNING.md) — the `--plan` interview loop and
  step-granularity refinement.
- [EXECUTION.md](EXECUTION.md) — per-iteration execute behaviour and exit
  codes.

## Layout

```
cmd/determined/   entry point: flag parsing, tool selection, signal handling, wiring
src/models/       Config, PlanConfig, Invocation, Outcome, Tool (typed value objects)
src/services/     Orchestrator (execute) + PlanOrchestrator (plan) + the I/O interfaces they depend on
src/clients/      real I/O implementations (exec, filesystem, prompter, clock, log files)
```

Each tool (`droid`/`pi`/`claude`) is a `models.Tool` that builds its own
command from the prompt, so the orchestrator stays tool-agnostic. I/O sits
behind interfaces with hand-written Fakes (no mocking frameworks), so the loop
logic is tested entirely without touching a real CLI or disk.

```bash
go test -cover ./...
```

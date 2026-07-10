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

### Plan mode (`--plan`, attended)

1. **Capture the goal** — your goal text (or the file you pointed at) is
   written to `GOAL.md`.
2. **Interview** — each round the tool either writes clarifying questions to
   `QUESTIONS.md` or, once it knows enough, a finished `PLAN.md` + `STEPS.md`.
   Questions are asked on your terminal and recorded in `ANSWERS.md`, then the
   tool runs again with your answers.
3. **Quality gate** — an independent assess/refine loop checks completeness,
   the task-specific template, ordering, step size, and concrete acceptance
   criteria (`REFINEMENTS.md`), up to `--max-step-passes` rounds.
4. **Done** — `PLAN.md` + `STEPS.md` are in place (exit `0`); run
   `./determined` to execute them.

See [PLANNING.md](PLANNING.md) for details.

### Execute mode (default, unattended)

Each iteration:

1. **Completion check** — every box checked *and* the audit's `STOP.md`
   present → exit `0`. A `STOP.md` that appears while steps remain is deleted
   and the loop continues.
2. **Budget check** — `--max-duration` / `-t` exhausted → exit `1`.
3. **Build the prompt** — re-parse `STEPS.md` and aim the tool at exactly the
   next unchecked step, injecting its `Done when:` criterion and the
   `NOTES.md` memory instructions.
4. **Invoke the tool** — output streams live and is teed to `logs/`; a single
   invocation is killed after `--max-iteration-duration`. Consecutive
   failures beyond `--max-consecutive-failures` abort the run (exit `1`).
5. **Verify** — a fresh reviewer invocation confirms each newly checked step's
   acceptance criterion, unchecking it (and recording why in `FIXES.md`) when
   it doesn't hold.
6. **Checkpoint** — each verified step is git-committed.
7. **Stall detection** — `--max-stalled-iterations` iterations in a row with
   no newly checked step → exit `3`.

Once every box is checked, independent **security**, **performance**, and
**reliability/maintainability** reviewers run before the final whole-plan
audit. A specialist records concrete findings in `FIXES.md` and reopens the
relevant step (or adds a remediation step), so the loop fixes the issue and
reruns all specialist gates. The final audit then judges the implementation
against `PLAN.md`: it either reopens unsatisfied steps or creates `STOP.md` —
the only way a run ends successfully.

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
| `--mvp`          | `false`  | Use a reduced quality gate for the smallest usable outcome. Requires `--plan`; incompatible with `--prototype`. |
| `--prototype`    | `false`  | Ask only blocking questions and skip quality refinement for fast experiments. Requires `--plan`; incompatible with `--mvp`. |
| `--max-step-passes` | `5`   | Max quality assess/refine rounds during planning. `0` disables refinement. **plan only**. |
| `--max-duration`, `-t` | `1h` | Wall-clock budget, checked between iterations. `0` = unlimited. |
| `--max-iteration-duration` | `15m` | Kill a single tool invocation after this long; the timeout counts as a failed invocation. `0` = unlimited. |
| `--max-consecutive-failures` | `3` | Abort after this many consecutive failed tool invocations; any success resets the count. |
| `--max-stalled-iterations` | `3` | Stop (exit `3`) after this many consecutive iterations check no new step. `0` disables stall detection. |
| `--verify`       | `true`   | After each newly checked step, run an independent verifier invocation that unchecks it (recording why in `FIXES.md`) if its acceptance criterion is not met. |
| `--specialized-reviews` | `true` | Before the final audit, run independent security, performance, and reliability/maintainability review gates. |
| `--git-checkpoint` | `true` | Git-commit the working tree after each verified step when running in a git repository. |
| `--log-dir`      | `logs`   | Directory for per-iteration log files.                          |
| `--version`      | —        | Print the binary's semantic version and exit.                  |

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

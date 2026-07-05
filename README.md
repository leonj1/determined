# determined

A Go orchestrator that runs an AI coding tool in a loop until the work is done.
It grew out of hardening this bash loop:

```bash
while [ ! -e STOP.md ]; do
  droid exec "Read PLAN.md and STEPS.md. Find the first step that has needs to be completed. Implement that step. Mark the step completed when you are done. Only work on one step. When there are no more steps then create STOP.md"
done
```

`determined` only **orchestrates** invocations — the AI tool still does all the
work. Unlike the one-liner, it does not trust the tool's word for progress:

- **Parsed progress** — `STEPS.md` is a markdown checkbox list that the
  orchestrator parses itself each iteration; a `STOP.md` created while
  unchecked steps remain is deleted and the loop continues.
- **Per-step prompts** — each invocation is aimed at exactly the next
  unchecked step, with its `Done when:` acceptance criterion injected.
- **Independent verification** — after a step is checked, a fresh reviewer
  invocation confirms the acceptance criterion actually holds, unchecking the
  step (and recording why in `FIXES.md`) when it does not.
- **Final audit** — once every box is checked, one more invocation audits the
  whole plan; only its approval (`STOP.md`) ends the run successfully.
- **Stall detection, retries, and timeouts** — no-progress iterations,
  consecutive failures, and single-invocation duration are all bounded.
- **Memory and checkpoints** — `NOTES.md` carries knowledge between otherwise
  independent invocations, and each verified step is git-committed.

It has two modes:

- **execute** (default) — the unattended loop above, against an existing
  `PLAN.md` / `STEPS.md`.
- **plan** (`--plan "<goal>"`) — an *attended* loop that produces those files
  from a one-line goal by interviewing you first (see [PLANNING.md](PLANNING.md)).

## Getting started

```bash
# 1. Build the binary
go build -o determined ./cmd/determined

# 2a. Update an installed release binary to the latest GitHub Release:
./determined update

# 2b. Already have PLAN.md / STEPS.md? Run the execute loop in that directory:
./determined

# 2c. Starting from a one-line goal? Plan it interactively first:
./determined --plan "build a todo CLI"
# Or seed planning from a longer file:
./determined --plan TODO.md
# ...answer the clarifying questions, then run the execute loop:
./determined
```

Pick a different AI tool with `--tool` (`droid`/`pi`/`claude`), override the
droid or claude model with `--model`, and bound unattended runs with
`--max-duration` (or `-t`). For more detail, see
[BUILD.md](BUILD.md), [PLANNING.md](PLANNING.md), and [EXECUTION.md](EXECUTION.md).

## Supported tools

Pick the AI coding CLI with `--tool`. Each iteration runs the tool's own
command form with the prompt built for that iteration (see
[EXECUTION.md](EXECUTION.md)):

| `--tool`           | Command run each iteration            |
|--------------------|---------------------------------------|
| `droid` (default)  | `droid exec "<prompt>" --auto high [--model <model>]` |
| `pi`               | `pi -p "<prompt>"`                     |
| `claude`           | `claude -p "<prompt>" --permission-mode acceptEdits [--model <model>]` |

`--model <model>` is optional and only applies to `droid` and `claude`. For
droid, pass a Factory model ID such as `claude-opus-4-7`; for claude, pass a
Claude model alias such as `opus` or a full model name. `pi` does not support
model overrides through `determined`, so `--tool pi --model ...` exits as a
usage error.

### Why `droid` runs with `--auto high`

`droid exec` needs an autonomy level to run unattended — without one it stops
on a permission prompt and the loop aborts on iteration 1. `determined` always
passes `--auto high`; the level is not user-configurable.

### Why `claude` runs with `--permission-mode acceptEdits`

`claude -p` (print mode) is non-interactive: if the tool hits a permission
prompt it cannot ask, so without a permission mode every iteration stalls or
exits before doing any work. `--permission-mode acceptEdits` auto-approves
file edits in the working directory, which is exactly what an unattended step
loop needs.

The trade-off: Claude can create and modify files in the project without a
human confirming each edit. It does **not** auto-approve arbitrary shell
commands (that would be `bypassPermissions`, which we deliberately avoid).
Run `determined` in a directory you are prepared to have edited — ideally a
clean git checkout, so every change is reviewable and revertible.

## Build & run

See [BUILD.md](BUILD.md) for build commands, runtime flags in action, and the
versioned release build. `determined update` fetches the latest GitHub Release
for the current platform and replaces the running binary when that release is
newer.

## Planning a goal

See [PLANNING.md](PLANNING.md) for the `--plan` interview loop and
step-granularity refinement.

## Execution

See [EXECUTION.md](EXECUTION.md) for the per-iteration execute behaviour and exit
codes.

## Flags

| Flag             | Default  | Purpose                                                        |
|------------------|----------|----------------------------------------------------------------|
| `--tool`         | `droid`  | AI coding CLI to run (`droid`/`pi`/`claude`).                   |
| `--model`        | —        | Optional model ID or alias for `droid` or `claude`; rejected with `pi`. |
| `--plan`         | —        | Describe a goal to plan interactively; produces `PLAN.md` + `STEPS.md` instead of running the execute loop. |
| `--max-step-passes` | `5`   | Max assess/breakdown rounds to shrink oversized steps during planning. `0` disables refinement. **plan only**. |
| `--max-duration`, `-t` | `1h` | Wall-clock budget, checked between iterations. `0` = unlimited. |
| `--max-iteration-duration` | `15m` | Kill a single tool invocation after this long; the timeout counts as a failed invocation. `0` = unlimited. |
| `--max-consecutive-failures` | `3` | Abort after this many consecutive failed tool invocations; any success resets the count. |
| `--max-stalled-iterations` | `3` | Stop (exit `3`) after this many consecutive iterations check no new step. `0` disables stall detection. |
| `--verify`       | `true`   | After each newly checked step, run an independent verifier invocation that unchecks it (recording why in `FIXES.md`) if its acceptance criterion is not met. |
| `--git-checkpoint` | `true` | Git-commit the working tree after each verified step when running in a git repository. |
| `--log-dir`      | `logs`   | Directory for per-iteration log files.                          |
| `--version`      | —        | Print the binary's semantic version and exit.                  |

## Commands

| Command               | Purpose                                                        |
|-----------------------|----------------------------------------------------------------|
| `determined update`   | Download the latest supported GitHub Release binary and replace the current executable. |

The protocol filenames (`PLAN.md` / `STEPS.md` / `STOP.md` / `NOTES.md` /
`FIXES.md`) are hardcoded; the prompt is rebuilt each iteration from the next
unchecked step in `STEPS.md`.

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

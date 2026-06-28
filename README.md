# determined

A Go orchestrator that runs an AI coding tool in a loop until the work is done.
It is a hardened port of this bash loop:

```bash
while [ ! -e STOP.md ]; do
  droid exec "Read PLAN.md and STEPS.md. Find the first step that has needs to be completed. Implement that step. Mark the step completed when you are done. Only work on one step. When there are no more steps then create STOP.md"
done
```

`determined` only **orchestrates** invocations — the AI tool still does all the
work. It adds the safety the one-liner lacks: failure handling, a time budget,
graceful shutdown, and per-iteration logging.

It has two modes:

- **execute** (default) — the unattended loop above, against an existing
  `PLAN.md` / `STEPS.md`.
- **plan** (`--plan "<goal>"`) — an *attended* loop that produces those files
  from a one-line goal by interviewing you first (see [PLANNING.md](PLANNING.md)).

## Getting started

```bash
# 1. Build the binary
go build -o determined ./cmd/determined

# 2a. Already have PLAN.md / STEPS.md? Run the execute loop in that directory:
./determined

# 2b. Starting from a one-line goal? Plan it interactively first:
./determined --plan "build a todo CLI"
# Or seed GOAL.md from an existing file:
./determined --plan "Read TODO.md"
# ...answer the clarifying questions, then run the execute loop:
./determined
```

Pick a different AI tool with `--tool` (`droid`/`pi`/`claude`) and bound
unattended runs with `--max-duration`. For more detail, see
[BUILD.md](BUILD.md), [PLANNING.md](PLANNING.md), and [EXECUTION.md](EXECUTION.md).

## Supported tools

Pick the AI coding CLI with `--tool`. Each iteration runs the tool's own
command form with the hardcoded prompt:

| `--tool`           | Command run each iteration            |
|--------------------|---------------------------------------|
| `droid` (default)  | `droid exec "<prompt>" --auto <level>` |
| `pi`               | `pi -p "<prompt>"`                     |
| `claude`           | `claude -p "<prompt>"`                 |

## Build & run

See [BUILD.md](BUILD.md) for build commands, runtime flags in action, and the
versioned release build.

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
| `--plan`         | —        | Describe a goal to plan interactively; produces `PLAN.md` + `STEPS.md` instead of running the execute loop. |
| `--max-step-passes` | `5`   | Max assess/breakdown rounds to shrink oversized steps during planning. `0` disables refinement. **plan only**. |
| `--max-duration` | `1h`     | Wall-clock budget, checked between iterations. `0` = unlimited. |
| `--log-dir`      | `logs`   | Directory for per-iteration log files.                          |
| `--version`      | —        | Print the binary's semantic version and exit.                  |
| `--auto`         | `medium` | `droid` autonomy level (`low`/`medium`/`high`), **droid only**. Required for unattended runs — without it `droid exec` stops on a permission prompt and the loop aborts on iteration 1. Ignored by `pi`/`claude`. |

The prompt and the `STOP.md` / `PLAN.md` / `STEPS.md` filenames are hardcoded,
matching the original bash script.

## Known limitation

The only guard against a tool that exits `0` forever without writing `STOP.md`
(e.g. it keeps "finishing" without marking a step complete) is the wall-clock
budget. Keep `--max-duration` bounded for unattended runs.

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

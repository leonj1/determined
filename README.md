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
  from a one-line goal by interviewing you first (see [Planning a goal](#planning-a-goal)).

## Supported tools

Pick the AI coding CLI with `--tool`. Each iteration runs the tool's own
command form with the hardcoded prompt:

| `--tool`           | Command run each iteration            |
|--------------------|---------------------------------------|
| `droid` (default)  | `droid exec "<prompt>" --auto <level>` |
| `pi`               | `pi -p "<prompt>"`                     |
| `claude`           | `claude -p "<prompt>"`                 |

## Build & run

```bash
go build -o determined ./cmd/determined
./determined                       # droid in a directory containing PLAN.md / STEPS.md
./determined --tool pi             # use the pi CLI instead
./determined --tool claude         # use the claude CLI instead
./determined --max-duration 2h     # raise the time budget
./determined --max-duration 0      # unlimited (bash parity; Ctrl+C is the only stop)
./determined --version             # print the semantic version and exit
./determined --plan "build a todo CLI"   # interview, then write PLAN.md / STEPS.md
```

## Planning a goal

`PLAN.md` and `STEPS.md` are inputs to the execute loop — you normally write
them yourself. `--plan` bootstraps them from a one-line goal instead. Because a
one-line goal is rarely detailed enough, `determined` lets the AI tool **ask you
clarifying questions first**, mediating a file-based interview:

```bash
./determined --plan "build a todo CLI" --tool claude
```

Each round, in the current working directory:

1. Your goal is written to `GOAL.md`.
2. The tool runs (in its non-interactive print mode) and either:
   - writes clarifying questions to `QUESTIONS.md` (a markdown list), or
   - writes a finished `PLAN.md` **and** `STEPS.md`.
3. If there are questions, `determined` asks you each one on the terminal,
   records the round in `ANSWERS.md`, clears `QUESTIONS.md`, and runs the tool
   again — now with your answers in hand.
4. Once `PLAN.md` and `STEPS.md` both exist, `determined` refines the steps for
   granularity (see below).
5. When refinement settles, planning is done (exit **0**). Run `./determined`
   (no `--plan`) to execute the steps.

### Step-granularity refinement

A plan is only useful if each step is small enough for the AI tool to implement
in a single pass. After a plan first exists, `determined` runs an extra
assess/breakdown loop:

1. **Assess** — the tool reads `STEPS.md` and writes any steps that are too large
   to implement in one pass to `OVERSIZED.md` (a markdown list), or the single
   word `NONE` if every step is already small enough.
2. If `OVERSIZED.md` is `NONE`/empty → refinement is done.
3. **Break down** — otherwise the tool rewrites `STEPS.md`, splitting the flagged
   steps into smaller, individually-implementable ones, and `determined`
   re-assesses. Repeat.

The loop is bounded by `--max-step-passes` (default `5`; `0` disables refinement
entirely) and by `--max-duration`. If the cap is reached before the steps
converge, the usable plan is left in place with a warning.

Unlike the execute loop, planning is **attended**: it reads your answers from
stdin. `--max-duration` still bounds it, guarding against a tool that keeps
asking forever. The protocol filenames (`GOAL.md` / `QUESTIONS.md` /
`ANSWERS.md` / `OVERSIZED.md` / `PLAN.md` / `STEPS.md`) are hardcoded.

### Versioned release build

`make build` compiles the binary inside `Dockerfile.build` and stamps it with a
semantic version, dropping the result at `bin/determined`:

```bash
make build                 # uses the seed in ./VERSION (1.0.0)
make build VERSION=1.2.3    # override the version
```

The semver seed lives in the `VERSION` file (major.minor). On every push to the
default branch, the `build` GitHub Actions workflow stamps the binary with
`MAJOR.MINOR.<run-number>`, uploads it as a workflow artifact, and publishes a
tagged GitHub Release.

## Behaviour

Each iteration, in the current working directory:

1. If `STOP.md` exists → exit **0** (done).
2. If the time budget is exhausted → exit **1**. The budget is checked *between*
   iterations, so an in-flight tool run always finishes first.
3. Otherwise run the selected tool's command, streaming its output live **and**
   teeing it to `logs/iter-NNNN-<timestamp>.log`.
4. If the tool exits non-zero → abort immediately, exit **1**.
5. `SIGINT` / `SIGTERM` → stop and exit **1**.

### Exit codes

| Code | Meaning                                            |
|------|----------------------------------------------------|
| `0`  | The tool created `STOP.md` (execute), or wrote `PLAN.md` + `STEPS.md` (plan). |
| `1`  | Any other termination: tool failure, budget exhausted, interrupted, or a stalled plan round. |
| `2`  | Usage error (e.g. an unsupported `--tool`).        |

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

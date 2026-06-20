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
```

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
| `0`  | The tool created `STOP.md` — the run completed.    |
| `1`  | Any other termination: tool failure, budget exhausted, or interrupted. |
| `2`  | Usage error (e.g. an unsupported `--tool`).        |

## Flags

| Flag             | Default  | Purpose                                                        |
|------------------|----------|----------------------------------------------------------------|
| `--tool`         | `droid`  | AI coding CLI to run (`droid`/`pi`/`claude`).                   |
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
src/models/       Config, Invocation, Outcome, Tool (typed value objects)
src/services/     Orchestrator + the I/O interfaces it depends on
src/clients/      real I/O implementations (exec, filesystem, clock, log files)
```

Each tool (`droid`/`pi`/`claude`) is a `models.Tool` that builds its own
command from the prompt, so the orchestrator stays tool-agnostic. I/O sits
behind interfaces with hand-written Fakes (no mocking frameworks), so the loop
logic is tested entirely without touching a real CLI or disk.

```bash
go test -cover ./...
```

# determined

A Go orchestrator that runs an AI coding tool (`droid`) in a loop until the
work is done. It is a hardened port of this bash loop:

```bash
while [ ! -e STOP.md ]; do
  droid exec "Read PLAN.md and STEPS.md. Find the first step that has needs to be completed. Implement that step. Mark the step completed when you are done. Only work on one step. When there are no more steps then create STOP.md"
done
```

`determined` only **orchestrates** invocations — the AI tool still does all the
work. It adds the safety the one-liner lacks: failure handling, a time budget,
graceful shutdown, and per-iteration logging.

## Build & run

```bash
go build -o determined ./cmd/determined
./determined                       # run in a directory containing PLAN.md / STEPS.md
./determined --max-duration 2h     # raise the time budget
./determined --max-duration 0      # unlimited (bash parity; Ctrl+C is the only stop)
```

## Behaviour

Each iteration, in the current working directory:

1. If `STOP.md` exists → exit **0** (done).
2. If the time budget is exhausted → exit **1**. The budget is checked *between*
   iterations, so an in-flight `droid` run always finishes first.
3. Otherwise run `droid exec --auto <level> "<prompt>"`, streaming its output
   live **and** teeing it to `logs/iter-NNNN-<timestamp>.log`.
4. If `droid` exits non-zero → abort immediately, exit **1**.
5. `SIGINT` / `SIGTERM` → stop and exit **1**.

### Exit codes

| Code | Meaning                                            |
|------|----------------------------------------------------|
| `0`  | `droid` created `STOP.md` — the run completed.      |
| `1`  | Any other termination: tool failure, budget exhausted, or interrupted. |

## Flags

| Flag             | Default  | Purpose                                                        |
|------------------|----------|----------------------------------------------------------------|
| `--max-duration` | `1h`     | Wall-clock budget, checked between iterations. `0` = unlimited. |
| `--log-dir`      | `logs`   | Directory for per-iteration log files.                          |
| `--auto`         | `medium` | `droid` autonomy level (`low`/`medium`/`high`). Required for unattended runs — without it `droid exec` stops on a permission prompt and the loop aborts on iteration 1. |

The prompt, the `droid` binary, and the `STOP.md` / `PLAN.md` / `STEPS.md`
filenames are hardcoded, matching the original bash script.

## Known limitation

The only guard against a `droid` that exits `0` forever without writing
`STOP.md` (e.g. it keeps "finishing" without marking a step complete) is the
wall-clock budget. Keep `--max-duration` bounded for unattended runs.

## Layout

```
cmd/determined/   entry point: flag parsing, signal handling, dependency wiring
src/models/       Config, Invocation, Outcome (typed value objects)
src/services/     Orchestrator + the I/O interfaces it depends on
src/clients/      real I/O implementations (exec, filesystem, clock, log files)
```

I/O sits behind interfaces with hand-written Fakes (no mocking frameworks), so
the loop logic is tested entirely without touching the real `droid` or disk.

```bash
go test -cover ./...
```

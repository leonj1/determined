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
- **Tamper guard** — a work invocation may change `STEPS.md` only by checking
  its own step's box; reworded steps, weakened `Done when:` criteria, or
  added/deleted boxes are reverted from a pre-iteration snapshot with a
  warning (see [EXECUTION.md](EXECUTION.md)).
- **Independent verification** — after a step is checked, a fresh reviewer
  invocation confirms the acceptance criterion actually holds, unchecking the
  step (and recording why in `FIXES.md`) when it does not.
- **Deterministic check gate** (`--check-cmd`) — optionally require a fixed
  shell command (e.g. `go test ./...`) to pass before a newly checked step
  counts; a failure unchecks the step mechanically, with no AI judgment
  involved.
- **Final audit** — once every box is checked, one more invocation audits the
  whole plan; only its approval (`STOP.md`) ends the run successfully.
- **Stall detection, retries, and timeouts** — no-progress iterations,
  consecutive failures, and single-invocation duration are all bounded.
- **Replan escalation** (`--max-replans`) — a step that keeps failing
  verification is usually too big, not impossible: before exiting stalled,
  one invocation replaces the stuck step with 2–4 smaller ones and the loop
  resumes.
- **Memory and checkpoints** — `NOTES.md` carries knowledge between otherwise
  independent invocations, and each verified step is git-committed.
- **Failed-attempt stashing** — a step rejected a second time has its failed
  attempt `git stash`ed (hash and diffstat recorded in `FIXES.md`), so the
  retry starts clean from the last verified checkpoint instead of building on
  broken work; the stashes are dropped once the step finally passes.
- **Run report** — every execute run, however it ends (success, stall,
  failure, interruption), writes a machine-readable `run-report.json`
  summarizing the outcome, exit code, step tally, per-step rejections, and
  iteration/wall-clock totals (see [EXECUTION.md](EXECUTION.md)).
- **Stall handoff report** — a run that exits stalled (exit `3`) also writes a
  human-readable `STALLED.md` naming the stuck step, why each attempt at it
  was rejected, and the stashed attempts to inspect — built mechanically from
  data the orchestrator already tracks, with no extra AI invocation (see
  [EXECUTION.md](EXECUTION.md)).
- **Exit notification** (`--notify-cmd`) — optionally run a shell command
  once when the run ends — success, stall, failure, even interruption —
  after the reports are written, with the outcome exported as `DET_*`
  environment variables; a failing hook is warned and ignored (see
  [EXECUTION.md](EXECUTION.md)).

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
# ...answer the clarifying questions, then run the execute loop:
./determined
```

Pick a different AI tool with `--tool` (`droid`/`pi`/`claude`) and bound
unattended runs with `--max-duration`. For more detail, see
[BUILD.md](BUILD.md), [PLANNING.md](PLANNING.md), and [EXECUTION.md](EXECUTION.md).

## Supported tools

Pick the AI coding CLI with `--tool`. Each iteration runs the tool's own
command form with the prompt built for that iteration (see
[EXECUTION.md](EXECUTION.md)):

| `--tool`           | Command run each iteration            |
|--------------------|---------------------------------------|
| `droid` (default)  | `droid exec "<prompt>" --auto high` |
| `pi`               | `pi -p "<prompt>"`                     |
| `claude`           | `claude -p "<prompt>" --permission-mode acceptEdits` |

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
| `--max-iteration-duration` | `15m` | Kill a single tool invocation after this long; the timeout counts as a failed invocation. `0` = unlimited. |
| `--max-consecutive-failures` | `3` | Abort after this many consecutive failed tool invocations; any success resets the count. |
| `--max-stalled-iterations` | `3` | Stop (exit `3`) after this many consecutive iterations check no new step. `0` disables stall detection. |
| `--verify`       | `true`   | After each newly checked step, run an independent verifier invocation that unchecks it (recording why in `FIXES.md`) if its acceptance criterion is not met. |
| `--git-checkpoint` | `true` | Git-commit the working tree after each verified step when running in a git repository. |
| `--check-cmd`    | —        | Shell command (run via `sh -c`) that must succeed after each iteration that checks a step; on failure the step is unchecked and the output tail recorded in `FIXES.md`. Empty disables the gate. |
| `--notify-cmd`   | —        | Shell command (run via `sh -c`) run once when the execute run ends — however it ends — after the reports are written, with the outcome exported as `DET_*` environment variables. A failure is warned and ignored. Empty disables the hook. |
| `--max-replans`  | `1`      | When the stall cap is hit, ask the tool to replace the stuck step with smaller steps instead of stopping, at most this many times per run. `0` disables replanning. |
| `--stash-attempts` | `true` | From a step's second rejection on, `git stash` the failed attempt (recording its hash and diffstat in `FIXES.md`) so retries start from the last verified checkpoint. Needs `--git-checkpoint` and a working tree that starts the run clean; otherwise retries build in place as before. |
| `--log-dir`      | `logs`   | Directory for per-iteration log files.                          |
| `--version`      | —        | Print the binary's semantic version and exit.                  |

The protocol filenames (`PLAN.md` / `STEPS.md` / `STOP.md` / `NOTES.md` /
`FIXES.md`) are hardcoded, as are the `run-report.json` summary every execute
run writes on exit and the `STALLED.md` handoff a stalled run writes; the
prompt is rebuilt each iteration from the next unchecked step in `STEPS.md`.

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

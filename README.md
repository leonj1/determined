# determined

**Run an AI coding CLI in a loop until the plan is actually done — verified, not taken on its word.**

You give `determined` a plan (`PLAN.md` + a `STEPS.md` checkbox list). Each
iteration it invokes your AI tool of choice (`droid`, `claude`, or `pi`) with
exactly the next unchecked step and its acceptance criterion. When the tool
checks a step off, an independent reviewer invocation verifies the criterion
actually holds — unchecking it and recording why in `FIXES.md` when it
doesn't. Every verified step is git-committed. Only a final whole-plan audit
can end the run successfully, and it is preceded by a documentation update and
independent security, performance, and reliability/maintainability reviews.

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
./determined init
```

**2. Plan** (skip if you already have `PLAN.md` / `STEPS.md`) — an *attended*
loop that asks clarifying questions, then writes the plan files:

```bash
./determined --plan "build a todo CLI"   # from a one-line goal
./determined --plan TODO.md              # or seed from a longer file
./determined --plan "test a todo UI" -prototype # minimal experimental plan
```

**3. Execute** (`-exec`) — the *unattended* loop, run in the directory
containing the plan (ideally a clean git checkout, since the tool edits files
freely):

```bash
./determined -exec                        # droid by default, 1h budget
./determined -exec --tool claude -t 2h    # different tool, bigger budget
./determined --plan "build a todo CLI" -exec  # plan, then execute in one run
```

Running `determined` with neither `--plan` nor `-exec` defaults to `-exec`.

**4. Watch it work** — per-iteration logs land in `logs/`, each verified step
becomes a git commit, and the run ends with an exit code: `0` success (audit
approved), `1` failure/budget/interrupt, `2` usage error, `3` stalled (see
[EXECUTION.md](EXECUTION.md)).

Every unattended `-exec` run also starts the live status server, prints its
URL, and records the session for `-link` and `-chat`. The execution still runs
when that observer server cannot bind.

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
6. **Generate an optional UI demo** — in interactive mode, run a distinct
   post-plan check. Only a trivial UI change that fits in one self-contained
   HTML file produces `DEMO.html`; when present, it appears beneath the Plan tab.
7. **Finish planning** — leave `PLAN.md` and `STEPS.md` in place and exit `0`,
   ready for `./determined -exec` to execute them. With `-exec` on the same
   command line, execution starts immediately after planning succeeds.

See [PLANNING.md](PLANNING.md) for details.

### Criteria mode (`--criteria`, attended)

An optional interview that captures user-approved BDD journey tests *before*
planning or execution, so the plan and the final audit have criteria you wrote
rather than criteria the planner invented:

1. **Describe a journey** — you describe a user journey on the terminal
   (pressing Enter instead finishes the session).
2. **Propose a test** — the description goes to `CRITERIA_REQUEST.md` and the
   tool drafts one Gherkin feature into `CRITERIA_DRAFT.md`.
3. **Give a verdict** — **accept** records the test in `CRITERIA.md` and asks
   for another journey; **modify** takes a revision note and redrafts; **skip**
   forgets the draft and asks for another journey; **end** keeps this test plus
   all prior acceptances and finishes; **cancel** discards every test from the
   session, restoring `CRITERIA.md` to its pre-session state.

When `CRITERIA.md` exists, planning must include steps that implement each test
as an automated test, and the execute loop's final audit will not create
`STOP.md` while any of the tests is missing or failing. `--criteria` combines
with `--plan` and `-exec` (the session runs first); see
[PLANNING.md](PLANNING.md) for details.

### Execution mode (`-exec`, unattended)

1. **Validate the inputs** — require `PLAN.md` and `STEPS.md`; delete a stale
   `STOP.md` when it cannot prove that the current run is complete.
2. **Check completion and budget** — exit `0` only when every step is checked
   and the final audit created `STOP.md`; exit `1` if `--max-duration` / `-t`
   is exhausted.
3. **Select the next step** — re-parse `STEPS.md`, choose exactly the next
   unchecked checkbox, and build a prompt containing its `Done when:`
   criterion plus the `NOTES.md` memory instructions. Exit `1` when the same
   step has consumed more than `--step-max-runtime` across invocations.
4. **Invoke the tool** — stream output live and tee it to `logs/`. Kill an
   invocation after `--max-iteration-duration`, retry failures, and exit `1`
   after `--max-consecutive-failures` consecutive failures.
5. **Verify completed work** — for every newly checked step, first use a fresh
   reviewer invocation to check the implementation is the simplest solution
   that satisfies the step, then another to test its acceptance criterion. A
   rejection by either reviewer unchecks the step and records the reason in
   `FIXES.md`.
6. **Checkpoint verified work** — git-commit each newly checked step that
   survives verification.
7. **Detect stalls** — exit `3` after `--max-stalled-iterations` consecutive
   iterations without a newly checked step; otherwise return to step 2.
8. **Update the documentation** — once all boxes are checked, bring the
   project's own docs (`README.md`, a `docs/` directory, and any other
   documentation the project already keeps) in line with what the work
   changed: new commands, flags, environment variables, endpoints,
   configuration, and setup steps.
9. **Run specialist reviews** — independently review security, performance,
   and reliability/maintainability. A concrete finding is written to
   `FIXES.md` and reopens a relevant step (or adds a remediation step),
   returning execution to step 2.
10. **Audit the whole plan** — after the specialist gates pass, compare the
    implementation with `PLAN.md`. Reopen unsatisfied steps and record why in
    `FIXES.md`, or create `STOP.md` when the entire plan is satisfied.
11. **Finish execution** — return to the completion check, which exits `0`
    only when all boxes remain checked and `STOP.md` is present.

See [EXECUTION.md](EXECUTION.md) for details.

### Agent chat (`-chat`)

Ask the currently running planning or execution session about its live state.
Answers are deterministic snapshots, not another AI invocation, and include a
structured JSON payload on the wire:

```bash
determined -chat                                      # conversation + live events
determined -chat -m "what is the status of this run?" # one reply, then exit
```

Chat uses the same verified session discovery as `-link`. The one-shot form
prints only human-readable answer text; persistent chat labels pushed activity
events. See [USAGE.md](USAGE.md) for the WebSocket and HTTP protocols, including
runnable curl examples.

## Activity steps

Every phase of a run reports itself as a timestamped activity entry (on the
terminal and in the status page). These are the steps you will see and what
each one is for:

| Step | Purpose |
|------|---------|
| `writing planning goal` | Write the supplied goal text (or goal file contents) to `GOAL.md` so the planner has a fixed statement of intent. |
| `planning project` | Invoke the AI tool to read the goal and either raise clarifying questions or write `PLAN.md` / `STEPS.md`. |
| `answering planning questions` | Relay each question from `QUESTIONS.md` to you on the terminal and record your responses in `ANSWERS.md` for the next planner pass. |
| `assessing plan` | Independently grade the drafted plan for completeness, ordering, step size, and concrete `Done when:` criteria, writing issues to `REFINEMENTS.md`. |
| `refining plan` | Rework the plan to resolve the assessment's issues, then reassess — up to `--max-step-passes` rounds. |
| `generating UI demo` | After the plan is final, create `DEMO.html` only when its UI change is trivial and can be shown without external dependencies. |
| `applying annotation` | Apply one user annotation from the plan page to the plan documents and republish them. |
| `executing step N` | Invoke the tool with exactly the next unchecked step from `STEPS.md` and its acceptance criterion. |
| `checking simplicity of step N` | Use a fresh reviewer invocation to judge whether the newly checked step's implementation is the simplest solution that satisfies it; a materially simpler alternative unchecks the step and records the simpler approach in `FIXES.md`. |
| `verifying step N` | Use a fresh reviewer invocation to test the newly checked step's `Done when:` criterion; failure unchecks it and records why in `FIXES.md`. |
| `checkpointing step N` | Git-commit the work of a step that survived verification. |
| `updating project documentation` | Once all boxes are checked, bring the project's own docs in line with what the work changed, before any review gate. |
| `running security review` | Independent specialist pass over the completed work for security findings; a material issue reopens or adds a remediation step. |
| `running performance review` | Independent specialist pass for performance findings, with the same reopen-on-finding behavior. |
| `running reliability and maintainability review` | Independent specialist pass for error handling, races, test gaps, and convention consistency, with the same reopen-on-finding behavior. |
| `auditing the whole plan` | Compare the implementation against `PLAN.md`; reopen unsatisfied steps, or create `STOP.md` when the entire plan is satisfied. |

While a task is running, the status page's Activity pane shows **Skip** and
**Stop** buttons next to the active entry; both ask for confirmation before
acting. Skip aborts just that task's tool invocation and the run moves on: a
skipped work step is checked off in `STEPS.md` without being done or verified
(the override is recorded in `NOTES.md`), and a skipped review is waived.
Stop aborts the active task and ends the whole run; completed steps stay
checked, so the page's Implement button (or rerunning determined) resumes
from the remaining ones.

## Supported tools

Pick the AI coding CLI with `--tool`. Each iteration runs the tool's own
command form with the prompt built for that iteration:

| `--tool`          | Command run each iteration                                             |
|-------------------|------------------------------------------------------------------------|
| `droid` (default) | `droid exec "<prompt>" --auto high [--model <model>]`                  |
| `pi`              | `pi -p "<prompt>"`                                                     |
| `claude`          | `claude -p "<prompt>" --permission-mode acceptEdits [--model <model>]` |

`--model` is optional and applies to planning, plan review, criteria capture,
and interactive planning as well as execution. Set `--exec-model` to use a
different model only for execution steps; when omitted, execution falls back
to `--model` or the CLI's default. Both flags support `droid` (Factory model
ID, e.g. `claude-opus-4-7`) and `claude` (alias like `opus` or a full model
name). Using either model flag with `--tool pi` exits as a usage error, and
`--exec-model` is rejected when the requested command has no execution phase.

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
| `--exec-model`   | —        | Optional model ID or alias used only for execution steps; falls back to `--model` when empty, is rejected with `pi`, and requires an execution phase. |
| `--plan`         | —        | Goal text or a file path to plan interactively; produces `PLAN.md` + `STEPS.md`. Add `-exec` to continue into execution once the plan is ready. |
| `--exec`         | `false`  | Run the execute loop against `PLAN.md` + `STEPS.md`. Add `--interactive` to seed the live page from existing planning documents, stream execution, and annotate then retry a failed run. With `--plan`, execution follows successful planning; incompatible with `--review-plan`. Without an operation flag, execution is the default. |
| `--review-plan`  | `false`  | Critique existing `PLAN.md` + `STEPS.md`, interview the user about consequential choices, and revise without executing. |
| `--criteria`     | `false`  | Interactively capture BDD journey tests into `CRITERIA.md` (accept / modify / skip / end / cancel per proposal). Runs before `--plan` / `-exec` when combined; incompatible with `--review-plan`. |
| `--interactive`  | `false`  | With `--plan` or `--exec`, serve a live HTML status page showing planning documents and workflow steps. A trivial, self-contained UI change may include a generated demo beneath the Plan tab. For an existing plan, execution starts immediately; after failure, annotate the page and click Implement to retry in the same session. After success, the Explanation tab (`/explain`) shows an AI-generated walkthrough with colored diffs, followed by a five-question Quiz tab (`/quiz`) whose questions link to their source explanation sections. |
| `--link`         | `false`  | Print the URL of the status page served by a live interactive or headless execution session, then exit. Prints the URL on exit `0` only after verifying the process, port, and determined status page; otherwise exits `1`. |
| `--chat`         | `false`  | Connect to the verified live session over WebSocket, subscribe to events, and exchange stdin lines for replies. Incompatible with run/session mode flags. |
| `-m`             | —        | With `--chat`, ask one question, print the reply text, and exit. Without `--chat`, exits with usage code `2`. |
| `--mvp`          | `false`  | Use a reduced quality gate for the smallest usable outcome. Requires `--plan`; incompatible with `--prototype`. |
| `--prototype`    | `false`  | Ask only blocking questions and skip quality refinement for fast experiments. Requires `--plan`; incompatible with `--mvp`. |
| `--max-step-passes` | `2`   | Max quality assess/refine rounds during planning or review. `0` disables refinement. |
| `--max-duration`, `-t` | `1h` | Wall-clock budget, checked between iterations. `0` = unlimited. |
| `--max-iteration-duration` | `15m` | Kill a single tool invocation after this long; the timeout counts as a failed invocation. `0` = unlimited. |
| `--step-max-runtime` | `15m` | Stop (exit `1`) when a single step's total runtime across invocations exceeds this, checked between invocations. `0` = unlimited. |
| `--max-consecutive-failures` | `3` | Abort after this many consecutive failed tool invocations; any success resets the count. |
| `--max-stalled-iterations` | `3` | Stop (exit `3`) after this many consecutive iterations check no new step. `0` disables stall detection. |
| `--verify`       | `true`   | After each newly checked step, run independent reviewer invocations — a simplicity check, then a correctness verification — either of which unchecks it (recording why in `FIXES.md`) if a materially simpler solution exists or its acceptance criterion is not met. |
| `--specialized-reviews` | `true` | Before the final audit, run independent security, performance, and reliability/maintainability review gates. |
| `--git-checkpoint` | `true` | Git-commit the working tree after each verified step when running in a git repository. |
| `--log-dir`      | `logs`   | Directory for per-iteration log files.                          |
| `--version`      | —        | Print the binary's semantic version and exit.                  |
| `init` (`-init`) | `false`  | Download the personal knowledge `CLAUDE.md` to `~/.claude/CLAUDE.md` and `AGENTS.md` to `~/AGENTS.md`, overwriting existing files. |

## Commands

| Command             | Purpose                                                        |
|---------------------|----------------------------------------------------------------|
| `determined update` | Download the latest supported GitHub Release binary and replace the current executable. |

### Recovering the live session URL

Interactive sessions and unattended execution runs print a status URL at
startup. Run `determined -link` from any terminal to recover it:

```
$ determined -link
http://localhost:63431/
```

The session's process ID and port are recorded in `~/.determined/session.json`,
but that record is only a starting point — a saved port proves nothing once the
process has exited and the operating system has handed the port to something
else. Before printing anything, `-link` confirms the recorded process is still
running, that its port is still listening, and that the port answers with the
determined status page rather than an unrelated server. If any check fails it
prints nothing to stdout, reports no running session on stderr, exits `1`, and
deletes the stale record so it cannot mislead a later call.

The protocol filenames (`PLAN.md` / `STEPS.md` / `DEMO.html` / `STOP.md` /
`NOTES.md` / `FIXES.md` / `EXPLANATION.md`) are hardcoded; the prompt is rebuilt each
iteration from the next unchecked step in `STEPS.md`. `EXPLANATION.md` is a
presentation artifact created only after successful interactive execution and
does not affect the run outcome.

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

The test suite requires Go 1.24 and Node.js 18 or newer; Node executes the
interactive status page's browser-behavior tests without third-party packages.

```bash
go test -cover ./...
```

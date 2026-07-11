# Planning a goal

`PLAN.md` and `STEPS.md` are inputs to the execute loop — you normally write
them yourself. `--plan` bootstraps them from a one-line goal instead. Because a
one-line goal is rarely detailed enough, `determined` lets the AI tool **ask you
clarifying questions first**, mediating a file-based interview:

```bash
./determined --plan "build a todo CLI" --tool claude
```

Normal planning applies a comprehensive quality gate. For deliberately lighter
work, select one alternative mode:

```bash
./determined --plan "ship the smallest useful todo CLI" -mvp
./determined --plan "test whether this UI idea works" -prototype
```

`-mvp` requires only the outcome, target use case, must-have scope, key
constraint, and observable core success. `-prototype` asks only questions that
block starting the experiment, permits simple manual acceptance checks, and
skips the post-plan refinement loop. The flags are mutually exclusive and only
valid with `--plan`.

For a longer goal kept in a file, pass the path instead of shell-expanding the
contents:

```bash
./determined --plan TODO.md --tool claude
./determined --plan "Read TODO.md" --tool claude
```

If you do use command substitution, quote it:

```bash
./determined --plan "$(cat TODO.md)" --tool claude
```

Unquoted backticks such as ``--plan `cat TODO.md` `` are split by the shell.
If the file starts with a Markdown heading, `determined` may receive only `#`
as the flag value.

In the current working directory:

1. Your goal is written to `GOAL.md`. If `GOAL.md` already exists,
   `determined` asks whether to use it instead of replacing it.
   If the `--plan` value is a file path, or `Read <path>`, that file's
   contents are copied into `GOAL.md` for the new session.
2. Each round, the tool runs (in its non-interactive print mode) and either:
   - writes clarifying questions to `QUESTIONS.md` (a markdown list), or
   - writes a finished `PLAN.md` **and** `STEPS.md`.

   `STEPS.md` must be machine-checkable, and the planning prompt enforces its
   format: a markdown checkbox list (one `- [ ]` item per step), every step
   ending with a `Done when:` line stating a concrete acceptance condition —
   a command to run or a behavior to observe. The execute loop parses these
   checkboxes to track progress, aim each iteration, verify completed steps,
   and detect stalls (see [EXECUTION.md](EXECUTION.md)).
3. If there are questions, `determined` asks you each one on the terminal,
   records the round in `ANSWERS.md`, clears `QUESTIONS.md`, and runs the tool
   again — now with your answers in hand.
4. Once `PLAN.md` and `STEPS.md` both exist, `determined` runs the plan quality
   gate (except in prototype mode; see below).
5. When refinement settles, planning is done (exit **0**). Run `./determined`
   (no `--plan`) to execute the steps.

Each stage is announced with a brief timestamped status, for example
`==> [2026-07-11 09:30:00] assessing plan`. Invocation statuses are also
recorded in their iteration log.

## Plan quality gate and refinement

A normal plan must identify its outcome, target user/use case, scope boundaries,
constraints, observable success, material risks, and validation approach. The
planner classifies the task (for example bugfix, feature, migration, API, UI, or
CLI) and applies the relevant template. MVP mode uses the reduced requirements
described above.

After a plan first exists, `determined` runs an independent assess/refine loop:

1. **Assess** — the tool checks quality-gate and task-template coverage, step
   ordering and size, unstated dependencies, and vague `Done when:` criteria.
   It writes actionable findings to `REFINEMENTS.md`, or `NONE` when the plan
   passes.
2. **Refine** — the tool resolves every finding in `PLAN.md` / `STEPS.md`, then
   `determined` assesses the result again.

The loop is bounded by `--max-step-passes` (default `5`; `0` disables refinement
entirely) and by `--max-duration` / `-t`. If the cap is reached before the steps
converge, the usable plan is left in place with a warning.

Unlike the execute loop, planning is **attended**: it reads your answers from
stdin. `--max-duration` / `-t` still bounds it, guarding against a tool that keeps
asking forever. The protocol filenames (`GOAL.md` / `QUESTIONS.md` /
`ANSWERS.md` / `REFINEMENTS.md` / `PLAN.md` / `STEPS.md`) are hardcoded.

## Reviewing an existing plan

Use review mode when `PLAN.md` and `STEPS.md` already exist and you want to
critique and revise them before execution:

```bash
./determined --review-plan --tool claude
```

The assessor checks scope, assumptions, edge cases, risks, sequencing,
dependencies, validation, and acceptance criteria. Findings whose resolution
depends on product intent, preference, or risk tolerance become terminal
interview questions. Answers are recorded in `REVIEW_ANSWERS.md`, then used as
authoritative input while revising `PLAN.md` and `STEPS.md`. The result is
assessed again until it passes or reaches `--max-step-passes`.

Review mode requires both plan files, never creates a new plan, and never enters
the execute loop. `--plan` and `--review-plan` are mutually exclusive; `--mvp`
and `--prototype` apply only to `--plan`.

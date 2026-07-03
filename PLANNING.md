# Planning a goal

`PLAN.md` and `STEPS.md` are inputs to the execute loop — you normally write
them yourself. `--plan` bootstraps them from a one-line goal instead. Because a
one-line goal is rarely detailed enough, `determined` lets the AI tool **ask you
clarifying questions first**, mediating a file-based interview:

```bash
./determined --plan "build a todo CLI" --tool claude
```

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
4. Once `PLAN.md` and `STEPS.md` both exist, `determined` refines the steps for
   granularity (see below).
5. When refinement settles, planning is done (exit **0**). Run `./determined`
   (no `--plan`) to execute the steps.

## Step-granularity refinement

A plan is only useful if each step is small enough for the AI tool to implement
in a single pass. After a plan first exists, `determined` runs an extra
assess/breakdown loop:

1. **Assess** — the tool reads `STEPS.md` and writes any steps that are too large
   to implement in one pass to `OVERSIZED.md` (a markdown list), or the single
   word `NONE` if every step is already small enough.
2. If `OVERSIZED.md` is `NONE`/empty → refinement is done.
3. **Break down** — otherwise the tool rewrites `STEPS.md`, splitting the flagged
   steps into smaller, individually-implementable ones — keeping the checkbox
   format and per-step `Done when:` lines — and `determined` re-assesses. Repeat.

The loop is bounded by `--max-step-passes` (default `5`; `0` disables refinement
entirely) and by `--max-duration`. If the cap is reached before the steps
converge, the usable plan is left in place with a warning.

Unlike the execute loop, planning is **attended**: it reads your answers from
stdin. `--max-duration` still bounds it, guarding against a tool that keeps
asking forever. The protocol filenames (`GOAL.md` / `QUESTIONS.md` /
`ANSWERS.md` / `OVERSIZED.md` / `PLAN.md` / `STEPS.md`) are hardcoded.

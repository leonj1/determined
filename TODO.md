# TODO: `-exec-model` flag

## Goal

Add a `-exec-model` CLI flag: an LLM model name used **solely for execution
steps** (the execute loop). All other phases (plan, review-plan, criteria,
interactive plan session) keep using the model from `-model`. When
`-exec-model` is not provided, execution uses the same tool/model as today
(the `-model` value, or the CLI's default when `-model` is also empty).

## Current state

- `cmd/determined/main.go:41` defines `-model`; `main.go:96-99` builds a single
  `models.Tool` via `models.SelectTool(name, ToolOptions{Model})` and passes
  that one instance everywhere: `runPlan`, `runReviewPlan`, `runCriteria`,
  `runInteractiveExec`, and `runLoop`.
- Execution steps are performed by `runLoop` (`main.go:338`), invoked from:
  1. the plain exec path (`main.go:146`),
  2. the post-plan execution path (`main.go:138`),
  3. the `executor` closures handed to `runPlan` (`main.go:133-135`) and
     `runInteractiveExec` (`main.go:141-143`).
- `models.SelectTool` (`src/models/tool.go:95`) rejects a non-empty model for
  `pi` and passes `--model <id>` through `withModel` for `droid`/`claude`.

## Plan

### 1. Flag registration (`cmd/determined/main.go`)
- [ ] Add `execModel := flag.String("exec-model", "", "model ID or alias used only for execution steps; falls back to -model when empty")` next to the existing `model` flag.

### 2. Tool selection (`cmd/determined/main.go`)
- [ ] Keep the existing `selected` tool built from `-model` for planning/review/criteria phases.
- [ ] Build a second tool for execution: when `*execModel` is non-empty, call
      `models.SelectTool(models.ToolName(*tool), models.ToolOptions{Model: models.ModelID(*execModel)})`;
      when empty, reuse `selected`. Extract a small helper (e.g.
      `selectExecTool(name string, execModel string, fallback models.Tool) (models.Tool, error)`)
      to stay under the 30-line function limit.
- [ ] Exit with code 2 and a clear message on selection error (mirrors the
      existing `-model` error path). The `pi` restriction ("model only
      supported with droid or claude") applies automatically via `SelectTool`.

### 3. Route the exec tool to execution call sites (`cmd/determined/main.go`)
- [ ] Pass the exec tool (not `selected`) to `runLoop` in:
  - the plain exec / default path (`main.go:146`),
  - the post-plan execution call (`main.go:138`),
  - the `executor` closure given to `runPlan` (`main.go:133-135`),
  - the `executor` closure given to `runInteractiveExec` (`main.go:141-143`).
- [ ] `runPlan`, `runReviewPlan`, `runCriteria`, and `runInteractiveExec`
      continue to receive `selected` (their `tool` parameter drives the
      planning/interactive session, not execution steps — execution goes
      through the injected `executor`).
- [ ] No signature changes required in `src/` — the split lives entirely in
      `main.go` wiring; `runLoop` already takes its tool as a parameter
      (constructor-style injection preserved).

### 4. Validation
- [ ] `-exec-model` with `-tool pi` fails (covered by `SelectTool`; add test).
- [ ] `-exec-model` given but no execution phase requested (e.g. only
      `-review-plan`) — follow the pattern of existing flag validators
      (`validateExecFlag`, `validateInteractiveFlag`) and reject with exit 2
      so the flag is never silently ignored. Add
      `validateExecModelFlag(execModel string, executing bool) error`.

### 5. Tests (`cmd/determined/main_test.go`, `tests/`)
- [ ] Exec tool selection: `-exec-model` set → execution tool identity carries
      the exec model; planning tool keeps `-model` value.
- [ ] Fallback: `-exec-model` empty → execution tool identical to `selected`.
- [ ] `pi` + `-exec-model` → error, exit 2.
- [ ] `-exec-model` without execution phase → validation error, exit 2.
- [ ] Existing `withModel` arg-building tests already cover `--model`
      pass-through; no changes needed there.

### 6. Docs
- [ ] Update README.md / EXECUTION.md flag docs with `-exec-model`.

## Out of scope

- Per-step model overrides (different models per execution step).
- Separate `-exec-tool` (tool binary stays the same; only model differs).
- Status page display of the exec model (follow-up if wanted — `ToolIdentity`
  already carries `Model`, so the interactive page would show it naturally).

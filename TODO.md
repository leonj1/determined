# TODO: `determined -exec -interactive` — live status page with annotate-and-retry

## Context

Today `-interactive` requires `-plan`: an exec-only run (`determined -exec`) is always
headless, and even in plan-interactive sessions a failed Implement run leaves the button
permanently dead (`RequestImplement` rejects once `ExecPhase != ""`,
`src/services/plan_status_service.go:208`). Observed pain: execution halted after 3 failed
step verifications, the user annotated the failing step on the page, and nothing could resume.

This change makes `determined -exec -interactive` serve the live status page for an existing
PLAN.md/STEPS.md: the page seeds Goal/Plan/Tests/Steps from disk (planning shown as
succeeded), execution starts immediately and streams live, and after a **failed** run the
Implement button re-arms so the user can annotate → apply → click Implement → retry, all in
one session. The re-arm also fixes the dead-button bug in the existing plan-interactive flow.

Agreed scope: full retry loop; seed docs from disk; re-arm on `failed` only (never after
success); bare `determined -interactive` stays rejected (requires explicit `-plan` or `-exec`).

Exploration finding: the plumbing is ~90% there — `runLoop` already accepts a live
`services.ExecStatusReporter` (the exec-only branch just passes `nil`), and
`PlanStatusService` already implements every exec event.

## Step 1 — Service: re-arm Implement after failure, fresh timing window on retry

`src/services/plan_status_service.go`:
- [ ] `RequestImplement` (:205): guard becomes
  `!st.ImplementOffered || st.Phase != models.PlanPhaseSucceeded || !execRetryable(st.ExecPhase)`
  with new helper `execRetryable(phase models.ExecPhase) bool` → true for `""` or
  `models.ExecPhaseFailed` (requested/running/succeeded stay blocked).
- [ ] `StartExecution` (:231): also `st.ExecEndedAt = time.Time{}` (add `time` import). Keep
  `ExecLog` cumulative across retries (honest history; progress bar derives from taskSteps,
  not ExecLog). Update doc comment.

Tests in `tests/plan_status_service_test.go` (black-box, `steppingClock`, exact-state):
- [ ] Re-arm after failed exec: Offer → Finish(true) → StartExecution → FinishExecution(false)
  → RequestImplement ⇒ `ExecPhase == requested` and one value receivable from `ImplementSignal()`.
- [ ] Stays blocked after successful exec: same with FinishExecution(true) ⇒ phase stays
  `succeeded`, no signal.
- [ ] Retry timing: second StartExecution ⇒ `ExecStartedAt` = latest clock,
  `ExecEndedAt.IsZero()`, prior ExecLog entry retained (settled to error state).

## Step 2 — Page: show Implement button after a failed run

- [ ] `src/clients/plan_status_page.html` (~:1134): visibility becomes
  `!!status.implementOffered && status.phase === "succeeded" && (!status.execPhase || status.execPhase === "failed")`.
  No other page changes: failed→requested→running re-render, banner, and progress already
  behave (verified renderExec/progressState paths).
- [ ] Test in `tests/plan_status_page_test.js`: render a `phase:"succeeded",
  implementOffered:true` status with `execPhase` of `"failed"` / `"running"` / `"succeeded"`
  / `""`; assert the `implement` button `on` class is true/false/false/true.
  No `tests/plan_status_server_test.go` marker changes needed (no renames).

## Step 3 — `PlanDocumentPublisher`: disk → snapshot seeding (shared with planning)

New `src/services/plan_document_publisher.go`:
- [ ] `PlanDocumentSink` interface: `SetGoal/SetPlan/SetTests/SetTaskSteps` (satisfied by
  `*PlanStatusService`).
- [ ] `PlanDocumentPublisher{files FileStore, cfg models.PlanConfig}` +
  `NewPlanDocumentPublisher`; methods `PublishGoal(sink)`, `PublishPlan(sink)` (plan + tests
  + steps), `Publish(sink)` (everything). Bodies move verbatim from
  `PlanOrchestrator.reportGoal/reportPlan/reportTests/reportTaskSteps`
  (`plan_orchestrator.go:713-758`), preserving exact read-error-skip semantics. Move
  unexported `taskSteps()` helper (:761-767) into this file (same package — the exec
  orchestrator's use keeps compiling).
- [ ] `src/services/plan_orchestrator.go`: add `docs *PlanDocumentPublisher` field (built in
  `NewPlanOrchestrator`); `reportGoal`/`reportPlan` delegate to it; delete moved helpers.
  Existing reporting/annotation tests must stay green unchanged.

New `src/services/plan_document_publisher_test.go` (in-package, reuse `fakeFileStore`; small
`fakeDocumentSink` recording calls):
- [ ] Seeds all documents: exact goal/plan/tests strings; STEPS.md with `- [x]`/`- [ ]`
  items parses to TaskSteps with correct Text/Completed.
- [ ] Missing files publish nothing (all recorded slices empty ⇒ page placeholders survive).
- [ ] Goal-only store publishes goal and nothing else.

## Step 4 — cmd wiring: flag, dispatch, `runInteractiveExec`

`cmd/determined/main.go`:
- [ ] `validateInteractiveFlag(interactive, planning, executing bool)` — error
  `"-interactive requires -plan or -exec"`; call site passes raw `*exec` (not
  `executeRequested`) so bare `-interactive` still errors. Update `-exec`/`-interactive`
  help text.
- [ ] Dispatch: exec-only branch splits — if `*interactive`, build the same `executor`
  closure as the -plan branch and call `runInteractiveExec(...)`; else headless
  `runLoop(..., nil, ...)` as today.
- [ ] Extract `createPlanConfig(tool, goal, mode, budget, maxStepPasses, maxFailures)
  models.PlanConfig` from `runPlan` (:398-419); exec path calls it with `goal=""`,
  `models.PlanModeStandard`, `maxStepPasses=0`— gives ServeFeedback/applyAnnotation/
  rebuildFromGoal every invocation they need.
- [ ] Extract `startStatusSession(status, clock) (server, cleanup func(), ok bool)` from
  `runInteractivePlan` (:440-452): server start, locator Remember + `-link` hint, cleanup =
  Forget + 2s-timeout Shutdown. Refactor `runInteractivePlan` onto it (keeps both <30 lines).
- [ ] Extract `serveFeedbackLoop(ctx, orchestrator, status, execute, outcome)` from
  `holdStatusPage` (:516-528) — the `for orchestrator.ServeFeedback(...) { outcome =
  execute(...) }` loop; `holdStatusPage` becomes offer + printf + delegate.
- [ ] New in `plan_flow.go`: `seedResumedSession(status, docs planDocumentPublisher)` →
  `status.Start(); docs.Publish(status); status.Finish(true)` (small interface
  `planDocumentPublisher{ Publish(services.PlanDocumentSink) }` for testability).
- [ ] New `runInteractiveExec(ctx, tool, budget, maxFailures, execute, clock, logs)` in
  main.go next to `runInteractivePlan`:
  - `cfg := createPlanConfig(tool, "", standard, budget, 0, maxFailures)`; `status :=
    services.NewPlanStatusService(clock, gitContext, tool.Identity())`;
    `startStatusSession`; construct `services.NewPlanOrchestrator(
    clients.NewExecCommandRunner(), files, clients.NewStdinPrompter(...), clock, logs,
    os.Stdout, cfg).WithStatusReporter(status)`.
  - `seedResumedSession(status, services.NewPlanDocumentPublisher(files, cfg))`.
  - `outcome := execute(ctx, status)`; on `ctx.Err()` return.
  - **Only then** `status.OfferImplement()` (offering before the first run would open a
    window — phase succeeded, ExecPhase still "" — where a click queues a spurious run) +
    "still serving … annotate / Implement to run again after a failure / Enter to exit" printf.
  - `return serveFeedbackLoop(ctx, orchestrator, status, execute, outcome)`.

Tests in `cmd/determined/main_test.go`:
- [ ] Update the three `validateInteractiveFlag` tests (:423-435) to the 3-arg form; add
  `TestUserCanUseInteractiveWithExec` (`true,false,true` accepted); assert the reject
  message names both `-plan` and `-exec`.
- [ ] `TestResumedSessionSeedsDocumentsAndShowsPlanningSucceeded`: `fakeDocsPublisher` +
  real `PlanStatusService` with `fixedClock`; after `seedResumedSession` assert `published`,
  `Phase == succeeded`, `StartedAt`/`EndedAt` set — proves the page renders a completed
  planning half.

## Step 5 — Docs + verification

- [ ] README.md flag table: `--interactive` row no longer "Requires `--plan`"; describe the
  exec resume/retry flow.
- [ ] `go build ./... && go vet ./... && gofmt -l . && go test ./...` (the Go test wrapper
  runs the node JS page tests), plus `node --test tests/plan_status_page_test.js` directly.
- [ ] Manual smoke: in a repo with an existing PLAN.md/STEPS.md (some steps unchecked), run
  `determined -exec -interactive`, confirm: page seeds docs + planning bar at 50%, exec
  streams, on failure the Implement button appears, an annotation on the failing step lands
  in STEPS.md, Implement re-runs, Enter exits. `determined -link` recovers the URL.

## Decided edge cases

- **Goal annotation triggers a full replan** (`rebuildFromGoal` deletes/regenerates
  PLAN/STEPS/TESTS): accepted — identical to held plan-interactive behavior; the full
  standard PlanConfig makes it work.
- **Missing PLAN.md/STEPS.md**: seeding publishes nothing; exec fails fast; button re-arms;
  retries keep failing until files exist. No special handling.
- **Enter during a run** dismisses after the run ends (dismissalChannel armed before loop) —
  matches existing holdStatusPage semantics.
- **Cumulative ExecLog**: all attempts visible in Execution tab; duration/"Executing since"
  use reset timestamps; progress resumes mid-bar from STEPS.md checkboxes — the desired
  resume story.
- **Plan-interactive dead-button bug**: fixed by Steps 1+2 alone (`ImplementOffered`
  persists; relaxed guard + page condition), zero cmd changes.

## Execution order

1 → 2 → 3 → 4 → 5. Steps 1–3 are each independently green; 4 depends on 3 (publisher/sink)
and semantically on 1–2 for end-to-end retry.

---

# TODO: status page — expand left content column to fill up to the activity pane

## Root cause

The left column of the status page is deliberately capped at 70ch in two places, and a Go
test locks that in:

1. `src/clients/plan_status_page.html:156` — the `main` grid:
   `grid-template-columns: minmax(0, 70ch) minmax(16rem, 22rem); justify-content: space-between;`
   The left track can never exceed 70ch; on wide screens `space-between` pushes the activity
   pane far right and leaves dead space between the columns instead of letting the left grow.
2. `src/clients/plan_status_page.html:168` — `#content` repeats the cap: `max-width: 70ch;`.
3. `tests/plan_status_server_test.go:223` — the editorial visual contract asserts the literal
   string `grid-template-columns: minmax(0, 70ch)`.

## Steps

- [ ] **Widen the grid column** (`plan_status_page.html:154-158`): left track becomes
  `minmax(0, 1fr)` and drop the now-meaningless `justify-content: space-between`:
  ```css
  main {
    margin: 0; padding: 2rem 0 0;
    display: grid; grid-template-columns: minmax(0, 1fr) minmax(16rem, 22rem); gap: 3rem;
    align-items: start;
  }
  ```
  The right pane keeps its 16rem–22rem band; the `1fr` left track absorbs everything from
  the far left up to the 3rem gap.
- [ ] **Remove the inner cap** (`plan_status_page.html:168`):
  `#content { display: grid; gap: 2rem; min-width: 0; }` — keep `min-width: 0`; it lets wide
  children (diff2html tables, code blocks) shrink instead of blowing out the grid.
- [ ] **Update the contract test** (`plan_status_server_test.go:223`): marker
  `"grid-template-columns: minmax(0, 70ch)"` → `"grid-template-columns: minmax(0, 1fr)"`.
- [ ] **Verify**: `go test ./tests/...`; load the page on a wide window and confirm content
  spans from the left edge (inside the body's 2rem padding) to the activity pane, diffs/code
  blocks still scroll internally, and the `max-width: 50rem` mobile breakpoint (line 166)
  still collapses to one column (already `1fr`, no change needed).

## Note

70ch is a readable-line-length cap and the page is prose-heavy (Georgia serif, editorial
styling), so paragraphs will get long lines on wide monitors. If full-width panels with
still-readable text is wanted later: keep `minmax(0, 1fr)` on the grid but apply `max-width`
only to paragraph-like elements.

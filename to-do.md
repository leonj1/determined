# Fix: `-plan -interactive -exec` loses the live Execution view

## Problem

With `-plan -interactive -exec`, the status page dies the moment planning
finishes, and the execute loop runs blind:

- `cmd/determined/main.go:131` passes `holdPage = !executing`, so
  `runInteractivePlan` never enters `holdStatusPage`, never calls
  `status.OfferImplement()`, and shuts the web server down
  (`main.go:413`) as soon as the orchestrator returns.
- `main.go:133` then starts the execute loop with a **nil**
  `ExecStatusReporter`, so nothing streams to the Execution tab — which
  is dead anyway because the server is gone.

Net effect: the Implement button never appears (correct — execution is
already requested) but the user also gets no Execution tab progress
(wrong — the page should stay up and stream the run).

## Intended behavior

When `-plan -interactive -exec` and planning ends with
`OutcomePlanReady`:

1. Keep the status web server running.
2. Start the execute loop automatically, passing the
   `PlanStatusService` as the `ExecStatusReporter`, so the Execution
   tab streams live (`execPhase` running → succeeded/failed).
3. After execution finishes, hold the page (same Enter-to-dismiss
   behavior as plan-only mode) so the user can inspect the Execution
   tab. The Implement button stays hidden because `execPhase` is
   non-empty (`plan_status_page.html:957`) and `RequestImplement`
   already rejects re-requests (`plan_status_service.go:206`).
4. `main.go` must NOT run the second `runLoop` at line 133 — execution
   already happened inside the interactive session. Non-interactive
   `-plan -exec` keeps the current path unchanged.

## Design

Introduce a small pure decision type so the branching is testable
without touching the orchestrator:

```go
// postPlanAction says what an interactive session does after planning.
type postPlanAction int

const (
    postPlanDismiss   postPlanAction = iota // planning failed: shut down
    postPlanOffer                            // plan-only: offer Implement button
    postPlanAutoExec                         // -exec: run the execute loop now
)

func postPlanActionFor(executing bool, outcome models.Outcome) postPlanAction
```

- `runPlan` / `runInteractivePlan`: replace the `holdPage bool`
  parameter with `executing bool` (callers currently derive
  `holdPage = !executing`, so this is the same information, undistorted).
- `runInteractivePlan`, after the orchestrator returns and before
  server shutdown, switches on `postPlanActionFor(...)`:
  - `postPlanOffer` → existing `holdStatusPage` path, unchanged.
  - `postPlanAutoExec` → `outcome = execute(ctx, status)`, then hold
    the page for viewing (reuse the Enter-dismissal wait; no
    `OfferImplement`).
  - `postPlanDismiss` → current failure behavior (server stops).
- `shouldExecuteAfterPlan` gains an `interactive` parameter (or the
  call site passes `executing && !interactive`) so `main.go:133` skips
  the duplicate headless run when the interactive session already
  executed. Prefer the explicit parameter:

```go
func shouldExecuteAfterPlan(executing, interactive bool, outcome models.Outcome) bool {
    return executing && !interactive && outcome == models.OutcomePlanReady
}
```

- Terminal message for the auto-exec path (parallel to the existing
  hold message at `main.go:426`):
  `determined: plan ready — executing now; status page streaming at <url>`

Extract the post-execution page hold into a helper so
`runInteractivePlan` stays under the 30-line function limit.

## Steps

1. Add `postPlanAction` + `postPlanActionFor` to
   `cmd/determined/main.go` (or a small `plan_flow.go` beside it).
2. Change `runPlan`/`runInteractivePlan` signatures: `holdPage bool` →
   `executing bool`; update call site `main.go:131` to pass
   `executing` directly.
3. Implement the `postPlanAutoExec` branch in `runInteractivePlan`:
   run `execute(ctx, status)`, then hold the page until Enter,
   then shut down the server (existing deferred shutdown).
4. Update `shouldExecuteAfterPlan` to take `interactive` and update
   the call at `main.go:132`.
5. Update the doc comments on `runPlan`, `runInteractivePlan`,
   `holdStatusPage` that currently describe `holdPage`.

## Tests

All in existing test files (fakes over mocks, per conventions):

- `cmd/determined/main_test.go`
  - `postPlanActionFor(false, OutcomePlanReady)` → `postPlanOffer`.
  - `postPlanActionFor(true, OutcomePlanReady)` → `postPlanAutoExec`.
  - `postPlanActionFor(true, OutcomePlanStalled)` → `postPlanDismiss`
    (execution must not start from a stalled plan).
  - `postPlanActionFor(false, OutcomePlanStalled)` → `postPlanDismiss`.
  - `shouldExecuteAfterPlan(true, false, OutcomePlanReady)` → true
    (non-interactive `-plan -exec` unchanged).
  - `shouldExecuteAfterPlan(true, true, OutcomePlanReady)` → false
    (interactive session already executed — no double run).
  - Update the three existing `shouldExecuteAfterPlan` tests
    (`main_test.go:184-196`) for the new signature.

- `tests/plan_status_service_test.go`
  - Auto-exec stream sequence: `StartExecution` then
    `FinishExecution(true)` on a service whose planning succeeded but
    `OfferImplement` was never called — snapshot shows
    `ExecPhase == succeeded` and `ImplementOffered == false`.
    Proves the Execution tab lights up without the button.
  - `RequestImplement` after `StartExecution` is a no-op
    (`ExecPhase` already non-empty) — guards against the page
    re-triggering execution if a stale button click arrives.

- Executor wiring (the actual bug — nil reporter): give the auto-exec
  branch a seam that `main_test.go` can drive with a fake
  `planExecutor` that records the `ExecStatusReporter` it received.
  Assert the fake received the session's `PlanStatusService` (not
  nil), and that the returned outcome propagates as
  `runInteractivePlan`'s outcome. If `runInteractivePlan` is too
  entangled with the concrete orchestrator to test directly, extract
  the branch into
  `runAutoExec(ctx, status ExecStatusReporter, execute planExecutor, wait <-chan struct{}) models.Outcome`
  and test that.

## Verification

1. `make test` (or `go test ./...`).
2. Manual: `determined -plan "demo goal" -interactive -exec` against a
   trivial goal — confirm the page survives planning, the Execution
   tab streams phases, the Implement button never appears, and Enter
   dismisses the page after the run.
3. Manual regression: `-plan -interactive` (no `-exec`) still offers
   the Implement button and executes on click.

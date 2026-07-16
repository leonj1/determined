## 1. Models

- [x] 1.1 Add `PlanSessionStatus` type in `src/models/` (git remote/branch, goal text, plan text, phase, ordered step entries with timestamps, start/end times, completion state success|failed|running)
- [x] 1.2 Unit tests for `PlanSessionStatus` JSON serialization and duration derivation

## 2. Clients

- [x] 2.1 Add `GitInfo` interface in `src/clients/` with production implementation reading remote URL (`git remote get-url origin`) and branch (`git rev-parse --abbrev-ref HEAD`) via the existing exec-runner pattern; missing remote/branch returns explicit placeholders, not errors
- [x] 2.2 Add Fake `GitInfo` under `tests/`
- [x] 2.3 Add `StatusServer` interface in `src/clients/` (Start, URL, Shutdown) with production implementation on `net/http` bound to `127.0.0.1:0`, serving the embedded page at `/` and SSE at `/events`
- [x] 2.4 Add Fake `StatusServer` under `tests/` recording lifecycle and published snapshots
- [x] 2.5 Contract/integration test for the production server: bind, fetch `/`, receive SSE snapshot, shutdown

## 3. Status service

- [x] 3.1 Add `PlanStatusService` in `src/services/`: holds current `PlanSessionStatus`, applies updates (set goal, set plan, append step, mark waiting-for-input, mark complete/failed with clock timestamps), broadcasts full snapshots to subscribers; new subscribers immediately receive current state
- [x] 3.2 Introduce `ProgressSink` so `writeProgress` output fans out to both the terminal writer and the status service without changing terminal behavior
- [x] 3.3 Unit tests with Fake clock: snapshot contents, step ordering, late-subscriber replay, completion timestamps and duration

## 4. Orchestrator integration

- [x] 4.1 Wire `PlanOrchestrator` transition points to the status service: goal written (push `GOAL.md` contents), plan produced (push `PLAN.md` contents), question prompt begins/ends (waiting-for-input step), each progress message
- [x] 4.2 Record planning start time before the first round and end time on completion or failure via injected `Clock`
- [x] 4.3 Orchestrator tests asserting the Fake status sink receives the expected event sequence for a successful session and a failed session

## 5. HTML page

- [x] 5.1 Create embedded static page (`go:embed`): header with git remote + branch, Goal section, Plan section, live step list, completion banner area; vanilla JS `EventSource` re-rendering on each snapshot
- [x] 5.2 Banner rendering: hidden while running; on completion shows success or failure styling with start time, end time, duration

## 6. CLI wiring

- [x] 6.1 Register `-interactive` flag in `cmd/determined/main.go`; usage error (non-zero exit, no server, no planning) when supplied without `-plan`
- [x] 6.2 Start the status server before planning, print its URL; server bind failure aborts with error before invoking the AI tool
- [x] 6.3 After planning completes in interactive plan-only mode, keep serving and wait for Enter/interrupt before exit, with a terminal message; when `-exec` follows, shut the server down after the planning phase
- [x] 6.4 Flag-combination tests in `cmd/determined/main_test.go`

## 7. Docs and verification

- [x] 7.1 Document `-interactive` in `PLANNING.md` and `README.md`
- [x] 7.2 Run `make test` and a manual end-to-end `-plan -interactive` session verifying live updates, git header, and completion banner

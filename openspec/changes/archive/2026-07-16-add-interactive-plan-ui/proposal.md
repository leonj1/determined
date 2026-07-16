## Why

Planning sessions run entirely in the terminal, and long-running rounds (goal writing, question rounds, plan assessment) offer no at-a-glance view of where the session stands. A live web page lets the user watch the goal, the evolving plan, and the current workflow steps in real time from a browser, without scrolling terminal output.

## What Changes

- Add a new `-interactive` boolean CLI flag, valid only together with `-plan`. Using it without `-plan` is a usage error.
- When `-plan -interactive` is given, `determined` starts a local HTTP server (loopback, ephemeral or configurable port) before planning begins and prints its URL to the terminal.
- The server serves a single HTML page that shows:
  - Top header: the git remote URL and current branch of the working directory.
  - The Goal (contents of `GOAL.md`).
  - The Plan (contents of `PLAN.md`, once written).
  - The workflow steps/progress messages the planning orchestrator emits, appended live as the AI tool works.
- The page updates in real time via server-sent events (no manual refresh).
- When planning completes, the page shows a clear "Planning complete" banner with the start time, end time, and total duration of the planning phase.
- Terminal interaction (answering clarifying questions) is unchanged; the web page is read-only observability.

## Capabilities

### New Capabilities
- `interactive-plan-ui`: Local web server and live HTML status page for `-plan` sessions — real-time goal/plan/step display, git remote+branch header, and a completion banner with start/end/duration.

### Modified Capabilities
<!-- none: existing planning behavior is unchanged; the UI observes it. Progress emission gains an additional sink, but the planning requirements themselves do not change. -->

## Impact

- `cmd/determined/main.go`: register `-interactive` flag, validate it requires `-plan`, wire server lifecycle around `runPlan`.
- `src/services/`: new service for the status page state (event broadcast); `PlanOrchestrator` progress output fans out to the web sink in addition to the terminal.
- `src/clients/`: new HTTP server client (I/O interface + production implementation) and a git info client (remote/branch lookup); Fakes under `tests/`.
- `src/models/`: types for planning session status (phase, timestamps, step events).
- No changes to the execute loop, planning prompts, or file-based interview protocol.
- No new external dependencies expected (Go standard library `net/http` and SSE).

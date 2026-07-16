## Context

`determined -plan` runs a file-mediated planning interview: `PlanOrchestrator` (src/services/plan_orchestrator.go) writes `GOAL.md`, invokes the AI tool in rounds, relays clarifying questions on the terminal, and finishes when `PLAN.md` and `STEPS.md` exist and pass assessment. Progress is currently reported only via `writeProgress` (src/services/progress.go) to a terminal `io.Writer`.

This change adds an optional, read-only live web view of that session, activated by a new `-interactive` flag.

## Goals / Non-Goals

**Goals:**
- `-interactive` flag valid only with `-plan`; usage error otherwise.
- Local HTTP server started before planning, serving one HTML page.
- Page shows git remote + branch in header, Goal, Plan, and live step/progress feed.
- Real-time updates without manual refresh.
- Completion banner with start time, end time, and duration when planning finishes (success or failure state visible).

**Non-Goals:**
- No web-based answering of clarifying questions (terminal remains the interaction channel).
- No support for `-exec` / `-criteria` / `-review-plan` sessions.
- No authentication (loopback only).
- No persistence of session history beyond the running process.

## Decisions

1. **Server-sent events (SSE) over WebSockets.** One-directional server-to-browser stream is exactly what SSE provides; standard library only, auto-reconnect for free in `EventSource`. WebSockets would need a dependency and buy nothing here.

2. **Single embedded HTML page (`go:embed`), no build step.** Page is static HTML+vanilla JS opening an `EventSource` to `/events`. Keeps the binary self-contained per project convention (statically linked Go binary).

3. **Event model: full-state snapshots, not diffs.** Each SSE message carries the whole `PlanSessionStatus` JSON (goal, plan text, step log, phase, timestamps, git info). Browser re-renders on each message. Simpler than diffing; payloads are small (plan files are KBs). Late-joining browsers get current state on first event.

4. **State hub as a service.** New `PlanStatusService` in `src/services/` holds the immutable-ish current `PlanSessionStatus` (src/models/) and broadcasts snapshots to subscribers. Orchestrator progress fans out: `writeProgress` gains an additional sink by wrapping the terminal writer — a `ProgressSink` interface implemented by the terminal writer (existing behavior) and by the status service. Constructor injection throughout; no globals.

5. **File contents pushed by the orchestrator, not polled.** The orchestrator already knows when it writes `GOAL.md` and when the tool produces `PLAN.md`; at those transition points it reads the files and updates the status service. Avoids fs-watch dependency and race-prone polling. Alternative considered: fsnotify watcher — rejected (new dependency, duplicate source of truth).

6. **Git info via a client interface.** New `GitInfo` interface in `src/clients/` with production implementation shelling out (`git remote get-url origin`, `git rev-parse --abbrev-ref HEAD`) through the existing exec-runner pattern; Fake under `tests/`. Missing remote/branch (not a repo, no remote) renders as "no remote" / "detached" — reported, not fatal.

7. **HTTP server behind an interface.** `StatusServer` interface in `src/clients/` (Start/URL/Shutdown), production implementation on `net/http` bound to `127.0.0.1:0` (ephemeral port; printed URL tells the user where to look). Fake under `tests/` records handlers/lifecycle. Server shut down via context on planning completion path — page stays served until process exit so the user can read the completion banner; shutdown happens when the process ends (or execute loop begins, if `-exec` follows — server stops after planning phase ends since scope is plan mode).

8. **Timing from `Clock`.** Start time captured before first orchestrator round, end time on completion; both flow through the existing `Clock` interface (src/clients/system_clock.go) so tests control them. Duration computed in the browser-facing model, not the page.

9. **Errors as results.** Server start failure returns an error result; per literal-requirements convention, `-interactive` failing to bind is a hard failure (no silent fallback to non-interactive).

## Risks / Trade-offs

- [Ephemeral port means URL changes each run] → URL printed prominently at session start; acceptable for local tooling. A `-interactive-port` flag is deliberately out of scope until requested.
- [Full-snapshot SSE grows with plan size] → Plans are small markdown files; if ever large, switch to per-section events without changing the page contract.
- [Server lifetime vs process exit] → If planning ends and process exits immediately (plan-only mode), the browser loses the page before user sees the banner. Mitigation: in plan-only interactive mode, after completion `determined` keeps serving and waits for Enter/Ctrl-C before exiting, with a terminal message saying so.
- [Terminal question prompts not mirrored on page] → Page shows a "waiting for terminal input" step entry so the user knows to return to the terminal.

## Migration Plan

Purely additive flag; no migration. Rollback = remove flag wiring.

## Open Questions

- None blocking. Port-override flag deferred until asked for.

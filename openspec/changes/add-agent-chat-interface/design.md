# Design: add-agent-chat-interface

## Context

A running interactive session already exposes almost everything an agent would ask about:

- `services.PlanStatusService` holds a `models.PlanSessionStatus` snapshot (goal, plan, phase, task checklist with completion, timestamped workflow steps, planning and execution logs, stop reason/advice) and offers `Snapshot()` plus `Subscribe()` for live updates — the same pair the SSE `/events` endpoint uses.
- `clients.PlanStatusServer` serves the page, `/events`, `/annotate`, and `/implement` on an ephemeral port bound at startup.
- `services.SessionLocator` (backing `-link`) finds the running session from a well-known record file and refuses stale claims via three probes: record validity, process liveness, and an HTTP check that the port answers with the status page.

What is missing: a machine protocol on the server, a client mode in the CLI, and server availability during headless `-exec` runs (today `runLoop` receives a nil status reporter and no server starts, so there is nothing to chat with in the most common mode).

The project is stdlib-only (`go.mod` declares no dependencies) and follows models/services/clients layering with constructor DI and fake-driven tests in `tests/`.

## Goals / Non-Goals

**Goals:**

- An agent can run `determined -chat -m "what is the status of this run?"` against any live session — planning, interactive exec, or headless exec — and get one answer on stdout, then the connection drops.
- `determined -chat` holds a persistent conversation: request/reply plus server-pushed activity events.
- Deterministic, tested answers derived from the status snapshot; no AI invocation inside the binary.
- Zero new Go module dependencies.

**Non-Goals:**

- The binary does not interpret natural language beyond a small intent table; the connecting agent is the intelligent party and gets structured data with every reply.
- No remote/authenticated access story beyond what the status page already has (loopback-oriented, unauthenticated). Chat does not widen the surface: it is read-only over state the page already serves.
- No control-plane verbs (pause, stop, annotate, implement) in this change — read/observe only. Control can be a follow-up change.
- No chat UI on the web page.

## Decisions

### D1: WebSocket via a minimal stdlib RFC 6455 implementation

`go.mod` has zero dependencies and the repo treats that as a feature. We control both endpoints (the `determined` server and the `determined -chat` client), traffic is loopback, and the protocol needs only: the HTTP upgrade handshake (`Sec-WebSocket-Accept` = base64(SHA-1(key+GUID))), unfragmented text frames, client-to-server masking, close frames, and ping/pong. No extensions, no compression, no fragmentation support (reject with close code 1003), frames capped at 1 MiB (close 1009). That is a few hundred lines in `src/clients/websocket.go` with direct frame-level tests.

*Alternative considered:* `github.com/coder/websocket` — battle-tested, but it would be the module's first dependency for a protocol subset we can verify exhaustively ourselves. Rejected to preserve the zero-dep property; revisit if interop with third-party clients (browsers, non-determined agents) becomes a requirement — the handshake is standard, so external clients already work with the minimal server as long as they avoid fragmentation.

### D2: `/chat` endpoint on the existing PlanStatusServer

The chat endpoint mounts on the same mux and port as the status page. Discovery, the session record, and the `-link` liveness probes then work unchanged: one port per session, one record file. A separate chat-only server would need a second record format and a second set of probes for no benefit.

### D3: JSON message protocol, request/reply with correlation IDs plus pushed events

All frames are JSON text. Client → server:

```json
{"id": "1", "type": "message", "text": "what is the status of this run?"}
{"id": "2", "type": "subscribe"}
```

Server → client:

```json
{"id": "1", "type": "reply", "text": "<human-readable answer>", "data": { ...structured payload... }}
{"type": "event", "event": "log", "text": "==> verifying step 3", "data": { ... }}
{"id": "2", "type": "error", "error": "unknown request type"}
```

`id` correlates replies to requests so a persistent client can interleave; events carry no `id`. The one-shot client sends a single `message` and waits for the matching `reply`. `data` always carries the machine-consumable form (see D4) so the agent never has to parse prose.

*Alternative considered:* plain text lines — simpler, but forces the agent to parse prose and cannot label events vs replies.

### D4: Deterministic answers from the snapshot via a small intent table

A new `services.ChatService` takes the `PlanStatusService` (through a narrow `ChatStatusSource` interface: `Snapshot()` + `Subscribe()`) and a clock. Incoming `message` text is matched against intents by keyword:

| Intent | Trigger examples | Reply data |
|---|---|---|
| `status` (default) | "status", "how is it going", anything unmatched | phase(s), elapsed duration, current activity, steps done/total, stop reason+advice if failed |
| `plan` | "plan", "goal" | goal text, plan markdown |
| `steps` | "steps", "progress", "checklist" | task checklist with completion flags |
| `activity` | "activity", "doing", "working" | last workflow step + last log entry (message, state, tail of body) |
| `log` | "log", "output" | recent log entries (bounded tail) |
| `help` | "help", "commands" | intent list |

Unmatched text falls back to `status` with the full snapshot attached, so an agent always gets something useful and can self-serve from `data`. This keeps the binary honest (no fake NLU) and fully unit-testable.

*Alternative considered:* invoking the configured AI tool to compose answers — rejected: slow, non-deterministic, burns the user's tokens, and the caller is already an AI.

### D5: `-chat` is a client mode, mutually exclusive with session modes

Flag handling in `main.go` mirrors `-link`: `-chat` (bool) and `-m` (string). Validation: `-m` requires `-chat`; `-chat` rejects `-plan`, `-exec`, `-review-plan`, `-criteria`, `-interactive`. The client resolves the session with the existing `SessionLocator` (which already verifies the process, port, and page), derives `ws://localhost:<port>/chat`, and connects.

- One-shot (`-m`): send, await the correlated reply (10 s deadline), print `text` to stdout, close, exit 0. Locator miss → the `ErrNoSession` message on stderr, exit 1. Usage errors → exit 2, matching existing conventions.
- Interactive: print a short banner, auto-`subscribe`, then a loop — stdin lines become `message` frames; replies and events print to stdout (events prefixed, e.g. `[event]`); EOF, SIGINT, or server close ends with exit 0.

### D6: Headless `-exec` starts the status server too

`runLoop`-only invocations currently pass a nil reporter. The headless path now constructs a `PlanStatusService`, starts the `PlanStatusServer`, records the session via `sessionLocator()`, and passes the service as the reporter — exactly what `runInteractiveExec` does, minus the post-run feedback/hold loop: when the run ends, the server shuts down and the record is forgotten immediately. The URL lines print as they do today for interactive runs, and `-link` starts working for headless runs as a side benefit. A bind failure degrades to the current behavior (log to stderr, run without a server) rather than failing the run — chat is an observer, never a reason an execution cannot proceed.

## Risks / Trade-offs

- [Hand-rolled WebSocket has protocol bugs] → Scope is deliberately tiny (no fragmentation/extensions/binary frames — reject with proper close codes), both endpoints are ours, and framing gets byte-level unit tests including masking, close handshake, and oversized-frame rejection.
- [Server listens on 0.0.0.0 and chat is unauthenticated] → Same exposure as the existing status page and SSE stream, and chat is read-only in this change; no new data class is exposed. Adding control verbs later must revisit auth.
- [Headless `-exec` newly binds a port and writes the session record] → Behavior change for scripted users. Mitigated by degrading gracefully on bind failure and keeping stdout format otherwise identical; the record is cleared on exit like interactive runs. Note the change in EXECUTION.md.
- [Two sessions on one machine share one record file] → Pre-existing `-link` limitation; `-chat` inherits it (latest session wins). Out of scope to fix here.
- [Agent sends huge or malformed frames] → 1 MiB frame cap, JSON decode errors answered with a typed `error` frame, per-connection write deadline so a stuck client cannot wedge the broadcaster.

## Migration Plan

Purely additive CLI surface; no data or config migration. Rollback = revert the commits. The headless-server behavior ships in the same change but is isolated in `main.go` wiring, so it can be reverted independently if it causes trouble.

## Open Questions

- None blocking. Control verbs (stop/pause/annotate over chat) and multi-session records are deliberate follow-ups.

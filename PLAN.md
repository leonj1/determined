# Plan: Agent Chat Interface (`-chat`)

Give AI agents a first-class, machine-readable way to converse with a running
`determined` session about the current plan, activities, and work being
performed — over WebSocket for conversations, over plain HTTP for one-shots.

```bash
determined -chat                                      # persistent conversation
determined -chat -m "what is the status of this run?" # sync one-shot: reply, then disconnect
```

## Why

`determined` runs are observable today only by humans: the interactive web
page renders live status, and `-link` recovers its URL. An AI agent
supervising a run has no programmatic interface — it would have to scrape the
HTML page or tail log files. This change adds one.

## Current state (what already exists)

- `services.PlanStatusService` holds a `models.PlanSessionStatus` snapshot
  (goal, plan, phase, task checklist, workflow steps, planning/execution logs,
  stop reason and advice) with `Snapshot()` and `Subscribe()` — the same pair
  the SSE `/events` endpoint uses.
- `clients.PlanStatusServer` serves the status page, `/events`, `/annotate`,
  and `/implement` on an ephemeral port.
- `services.SessionLocator` (backing `-link`) finds the running session from a
  well-known record file and rejects stale claims via three probes: record
  validity, process liveness, and an HTTP check of the served page.

What is missing: a machine protocol on the server, a client mode in the CLI,
and server availability during headless `-exec` runs (today the plain exec
path passes a nil status reporter and starts no server).

## What changes

- **`-chat` CLI mode** — locates the running session with the same verified
  discovery as `-link`, opens a WebSocket to it, and holds a conversation:
  stdin lines go to the session; replies and live activity events stream to
  stdout until EOF/interrupt. Exit 0 on a clean close.
- **`-m <text>` flag** (valid only with `-chat`) — synchronous one-shot:
  connect, send, print the reply, close, exit. Exit codes: `0` success, `1`
  no live session or transport failure, `2` usage error.
- **`/chat` WebSocket endpoint** on the existing status server, speaking a
  JSON protocol (below).
- **`POST /chat/ask` HTTP endpoint** — answers a single question with the
  identical JSON reply, so curl and minimal HTTP clients need no WebSocket.
- **Headless `-exec` serves chat too** — plain `-exec` runs start the status
  server, print its URL, and write the session record (mirroring interactive
  startup, minus the hold/feedback loop), so agents can query unattended
  executions — the primary use case. A bind failure degrades gracefully: the
  run proceeds without a server.
- **USAGE.md** at the repo root — end-to-end protocol reference with runnable
  curl examples.
- **Zero new dependencies** — a minimal stdlib RFC 6455 implementation
  (server + client) in `src/clients`.

## Protocol

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

`id` correlates replies to requests; pushed events carry no `id`. Every reply
carries both prose and the structured `data` it was derived from, so the
agent never parses prose.

## Key design decisions

1. **Deterministic answers, no AI in the loop.** A new `services.ChatService`
   answers from the live status snapshot via a small intent table — status
   (default and fallback), plan/goal, steps/progress, activity, log, help.
   Unmatched text gets a status reply with the full snapshot in `data`. The
   caller is already an AI; the binary just reports state. Fast, free, and
   fully unit-testable.
2. **Hand-rolled minimal WebSocket, stdlib only.** `go.mod` has zero
   dependencies and both endpoints are ours on loopback. Scope: upgrade
   handshake, unfragmented text frames, client masking, ping/pong, close
   handshake. Fragmented/binary frames rejected with close 1003; frames over
   1 MiB with close 1009. Fallback if third-party interop ever matters:
   `github.com/coder/websocket`.
3. **`/chat` mounts on the existing status server** — one port, one session
   record, and the `-link` probes work unchanged.
4. **`/chat/ask` reuses `ChatService.Answer` verbatim** — one thin handler
   (405 on non-POST, 400 on malformed JSON) that keeps USAGE.md's curl
   examples honest, since curl's native WebSocket support is experimental.
5. **Read-only in v1.** Chat exposes only what the status page already
   serves; control verbs (stop, annotate, implement) are a follow-up change
   that must revisit auth.

## Risks and mitigations

- *Hand-rolled WebSocket bugs* → deliberately tiny protocol subset with
  byte-level tests (handshake accept value, masking, close codes, ping/pong).
- *Unauthenticated endpoint* → same exposure class as the existing status
  page and SSE stream; chat is read-only in this change.
- *Headless `-exec` newly binds a port and writes the session record* →
  degrade on bind failure, keep stdout otherwise identical, clear the record
  on exit; note the behavior change in EXECUTION.md.
- *One session record per machine* → pre-existing `-link` limitation; `-chat`
  inherits it (latest session wins). Out of scope here.
- *Hostile or broken clients* → 1 MiB frame cap, typed `error` frames for
  malformed JSON, per-connection write deadlines.

## Implementation steps

### 1. Chat protocol models
- [ ] 1.1 `src/models/chat.go`: `ChatRequest` (`ID`, `Type` = `message`|`subscribe`, `Text`), `ChatResponse` (`ID`, `Type` = `reply`|`event`|`error`, `Text`, `Event`, `Error`, `Data`), typed intent constants, validation helpers; tests in `src/models/chat_test.go`.

### 2. Minimal WebSocket transport (stdlib only)
- [ ] 2.1 `src/clients/websocket.go`: server-side RFC 6455 upgrade (header validation, `Sec-WebSocket-Accept`, connection hijack) and frame codec — unfragmented text frames, masking, ping/pong, close handshake; close 1003 for fragmented/binary, 1009 over 1 MiB.
- [ ] 2.2 Client-side dial in the same file: upgrade request with random key, accept-key verification, masked writes, unmasked reads.
- [ ] 2.3 Byte-level tests in `tests/websocket_test.go`: RFC example handshake key, mask round trip, close-code behavior, ping→pong.

### 3. Chat service (deterministic answers)
- [ ] 3.1 `src/services/chat_service.go`: `ChatStatusSource` interface (`Snapshot()`, `Subscribe()`), `Answer(ChatRequest) ChatResponse` implementing the intent table; replies carry text plus structured data (phase, elapsed, steps done/total, stop reason/advice, bounded log tail).
- [ ] 3.2 Tests with a fake status source: every intent, fallback with full snapshot, unknown type → `error` with matching `id`, planning vs executing vs failed-run answers.

### 4. Server endpoints
- [ ] 4.1 Mount `/chat` on `PlanStatusServer`: upgrade, read loop dispatching to the chat responder; `subscribe` forwards snapshot-diff events (new step, new log entry, phase change) as `event` frames; malformed JSON answered in-band; write deadline per send.
- [ ] 4.2 End-to-end tests over a real listener: upgrade, message/reply correlation, subscribe then observe an event, malformed payload keeps the connection open.
- [ ] 4.3 Mount `POST /chat/ask`: JSON body in, same JSON reply out; 405 non-POST, 400 malformed; tests alongside 4.2.

### 5. Chat client mode
- [ ] 5.1 `src/services/chat_client.go`: session resolution via `SessionLocator` → `ws://localhost:<port>/chat`; one-shot send/await with deadline; interactive loop over injected reader/writer.
- [ ] 5.2 Flags in `cmd/determined/main.go`: `-chat`, `-m`; validation (`-m` requires `-chat`; `-chat` excludes `-plan`/`-exec`/`-review-plan`/`-criteria`/`-interactive`); exit codes 0/1/2; `ErrNoSession` messaging.
- [ ] 5.3 Tests: flag validation table; one-shot and interactive flows against an in-process server, including reply timeout → exit 1 and server close → exit 0.

### 6. Headless -exec serves chat
- [ ] 6.1 Give the plain `runLoop` path a `PlanStatusService` + `PlanStatusServer` + session record (mirroring interactive startup without the hold loop); print URL lines; shut down and forget the record when the run ends.
- [ ] 6.2 Degrade on bind failure: report to stderr, run with a nil reporter; test that outcome and exit codes are unchanged.

### 7. Documentation
- [ ] 7.1 README: `-chat` section with persistent and one-shot examples; note headless `-exec` now serves the status/chat endpoints.
- [ ] 7.2 EXECUTION.md: chat exit codes, discovery shared with `-link`, protocol sketch for agent authors.
- [ ] 7.3 USAGE.md at the repo root: discovery, both CLI modes, field-by-field protocol reference, and runnable curl examples with sample responses — `curl -s -X POST http://localhost:<port>/chat/ask -d '{"text":"what is the status of this run?"}'`, `curl -N http://localhost:<port>/events` for the live SSE stream, and a WebSocket handshake check (plus a `curl ws://` note for builds with WebSocket support).

---

Full spec-driven artifacts (proposal, design, per-requirement scenarios,
tasks) live in `openspec/changes/add-agent-chat-interface/`.

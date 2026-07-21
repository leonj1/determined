# Proposal: add-agent-chat-interface

## Why

`determined` runs are observable today only by humans: the interactive web page renders live status, and `-link` recovers its URL. An AI agent supervising a run (or a user's coding assistant asked "how is that run going?") has no programmatic way to ask the running binary about the current plan, activities, or work being performed — it would have to scrape the HTML page or tail log files. A first-class machine interface lets agents converse with a live session directly.

## What Changes

- New `-chat` CLI mode: `determined -chat` locates the running session (reusing the `-link` discovery machinery), opens a WebSocket connection to it, and holds an interactive conversation — stdin lines go to the session, replies and live activity events stream back to stdout until EOF/interrupt.
- New `-m <message>` flag (valid only with `-chat`): synchronous one-shot — connect, send the single message, print the reply, close the connection, exit. Exit code reflects success (`0`), no live session or transport failure (`1`), usage error (`2`).
- The interactive status server gains a `/chat` WebSocket endpoint speaking a JSON message protocol. Replies are deterministic, derived from the live session snapshot (phase, current step, task checklist, recent activity, plan/goal text) — the binary reports state; the agent on the other end supplies the intelligence. Every reply carries both human-readable text and the structured data it was derived from.
- Persistent connections may subscribe to live events: step progress, log entries, phase transitions are pushed as they happen.
- Headless `-exec` runs (no `-interactive`) also start the status server and write the session record, so agents can chat with unattended executions — the primary "what is the status of this run?" use case. The server URL line is printed as it is for interactive runs.
- Minimal RFC 6455 WebSocket support (server and client, text frames, loopback use) implemented in `src/clients` with the standard library, preserving the project's zero-dependency `go.mod`.

## Capabilities

### New Capabilities

- `agent-chat-interface`: the `-chat` / `-m` CLI mode, session discovery and connection, the `/chat` WebSocket endpoint, the JSON request/reply/event protocol, and deterministic answers built from live session status.

### Modified Capabilities

<!-- interactive-plan-ui requirements are unchanged: the status page, SSE stream,
     and annotation flow keep their behavior; /chat is an additive endpoint
     covered by the new capability. -->

## Impact

- `cmd/determined/main.go`: new `-chat` and `-m` flags, mutual-exclusion validation against `-plan`/`-exec`/`-review-plan`/`-criteria`/`-interactive`, chat client entrypoint; headless `-exec` path gains status server startup + session recording.
- `src/clients`: new WebSocket handshake/framing (server + client), `/chat` handler on `PlanStatusServer`, chat client transport.
- `src/services`: new chat service answering queries from `PlanStatusService` snapshots and forwarding its subscription events.
- `src/models`: chat protocol message types.
- `tests/`: protocol, handshake, discovery-reuse, and one-shot flow coverage following the existing fake-driven style.
- Docs: README (`-chat` usage), EXECUTION.md (exit codes for chat mode).
- Dependencies: none added (stdlib-only WebSocket implementation).

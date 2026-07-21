# agent-chat-interface Specification (delta)

## ADDED Requirements

### Requirement: Chat flag gating
The CLI SHALL accept a `-chat` boolean flag and a `-m <text>` string flag. `-m` SHALL be valid only in combination with `-chat`. `-chat` SHALL be rejected in combination with `-plan`, `-exec`, `-review-plan`, `-criteria`, or `-interactive`. Invalid combinations SHALL exit with the usage exit code (2) without contacting any session.

#### Scenario: One-shot chat accepted
- **WHEN** the user runs `determined -chat -m "what is the status of this run?"`
- **THEN** the client attempts session discovery and no usage error is reported

#### Scenario: Message without chat
- **WHEN** the user runs `determined -m "hello"` without `-chat`
- **THEN** the program exits with code 2 and a usage error naming the invalid combination

#### Scenario: Chat combined with a session mode
- **WHEN** the user runs `determined -chat -exec`
- **THEN** the program exits with code 2 and a usage error, and no session or server is started

### Requirement: Session discovery for chat
`-chat` SHALL locate the running session using the same verified discovery as `-link`: the session record is trusted only after the recorded process is confirmed running, its port is listening, and the port answers as a determined status server. When no live session is confirmed, the program SHALL print the no-session error to stderr and exit with code 1.

#### Scenario: Live session found
- **WHEN** a `determined` session is serving its status server on a recorded port and the user runs `determined -chat -m "status"`
- **THEN** the client connects to that session's chat endpoint

#### Scenario: No live session
- **WHEN** no session record exists, or the recorded process is dead, and the user runs `determined -chat`
- **THEN** the program prints "no running interactive session found" to stderr and exits with code 1

### Requirement: Chat WebSocket endpoint
The status server SHALL serve a WebSocket endpoint at `/chat` performing a standard RFC 6455 upgrade handshake. Frames SHALL be JSON text messages. The server SHALL reject fragmented or binary frames with close code 1003 and frames larger than 1 MiB with close code 1009. A malformed JSON payload SHALL be answered with a typed `error` message on the open connection rather than a disconnect.

#### Scenario: Successful upgrade
- **WHEN** a client sends a valid WebSocket upgrade request to `/chat`
- **THEN** the server completes the handshake with the correct `Sec-WebSocket-Accept` value and holds the connection open

#### Scenario: Malformed payload
- **WHEN** a connected client sends a text frame that is not valid JSON
- **THEN** the server replies with an `error` message and keeps the connection open

### Requirement: Request and reply protocol
Client messages SHALL carry an `id`, a `type` of `message` or `subscribe`, and for `message` a `text` field. Each `message` SHALL receive exactly one `reply` carrying the same `id`, a human-readable `text` answer, and a structured `data` payload derived from the live session snapshot. An unknown `type` SHALL receive an `error` message carrying the same `id`.

#### Scenario: Status question answered
- **WHEN** a client sends `{"id":"1","type":"message","text":"what is the status of this run?"}` during an executing session
- **THEN** the server replies with `id` "1", text describing the phase, elapsed time, and step progress, and data containing the machine-readable equivalents

#### Scenario: Unknown request type
- **WHEN** a client sends `{"id":"9","type":"dance"}`
- **THEN** the server replies with an `error` message carrying `id` "9"

### Requirement: Deterministic snapshot-derived answers
Replies SHALL be computed from the current session status snapshot without invoking any AI tool. The server SHALL recognize at least these intents from message text: status (default and fallback), plan/goal, steps/progress, current activity, recent log, and help. Text that matches no intent SHALL be answered as a status reply with the full snapshot attached in `data`.

#### Scenario: Steps intent
- **WHEN** a client asks about "progress" while 3 of 5 task steps are completed
- **THEN** the reply lists the task checklist with per-step completion and data reports 3 of 5 complete

#### Scenario: Unmatched text falls back
- **WHEN** a client sends free-form text matching no intent keyword
- **THEN** the reply is a status answer and `data` carries the full session snapshot

### Requirement: Live event subscription
After a client sends a `subscribe` request, the server SHALL push `event` messages (without an `id`) for session changes — new workflow steps, log entries, and phase transitions — until the connection closes. A client that never subscribes SHALL receive replies only.

#### Scenario: Subscribed client receives activity
- **WHEN** a subscribed client is connected and the orchestrator emits a new progress step
- **THEN** the client receives an `event` message describing that step without having sent a request

### Requirement: One-shot synchronous mode
With `-chat -m <text>`, the client SHALL connect, send the single message, wait for the correlated reply, print the reply's human-readable text to stdout, close the connection, and exit 0. A reply not received within the timeout, or a transport failure, SHALL exit 1 with an error on stderr.

#### Scenario: One-shot round trip
- **WHEN** the user runs `determined -chat -m "what is the status of this run?"` against a live session
- **THEN** the answer text is printed to stdout, the connection is closed, and the exit code is 0

#### Scenario: Reply timeout
- **WHEN** the server accepts the connection but never replies within the deadline
- **THEN** the client exits with code 1 and reports the timeout on stderr

### Requirement: Interactive chat mode
With `-chat` and no `-m`, the client SHALL subscribe to live events and enter a loop: each stdin line is sent as a `message`, and replies and pushed events are printed to stdout with events visibly distinguished from replies. EOF on stdin, an interrupt, or a server-initiated close SHALL end the session with exit 0.

#### Scenario: Conversational loop
- **WHEN** the user (or agent) types "what step are you on?" followed by "show me the plan"
- **THEN** each line receives its printed reply in order, with any interleaved events labeled as events

#### Scenario: Session ends
- **WHEN** the serving session shuts down while a chat client is connected
- **THEN** the client reports the closed session and exits with code 0

### Requirement: HTTP one-shot ask endpoint
The status server SHALL serve `POST /chat/ask` accepting a JSON body with a `text` field and answering with the same JSON reply structure the WebSocket protocol uses (`text` plus structured `data`), so any HTTP client — curl included — can ask a one-shot question without speaking WebSocket. Non-POST methods SHALL be answered with 405 and malformed JSON with 400.

#### Scenario: curl asks for status
- **WHEN** a client runs `curl -s -X POST http://localhost:<port>/chat/ask -d '{"text":"what is the status of this run?"}'` against a live session
- **THEN** the response is a JSON reply with a human-readable `text` answer and the structured `data` payload

#### Scenario: Malformed body
- **WHEN** a client POSTs a body that is not valid JSON to `/chat/ask`
- **THEN** the server responds with status 400

### Requirement: Usage documentation
The repository SHALL include a USAGE.md documenting the chat interface end to end: session discovery, both CLI modes (`-chat` and `-chat -m`), the JSON message/reply/event protocol with field explanations, and runnable curl examples covering the `/chat/ask` one-shot, the `/events` SSE stream, and the WebSocket handshake, each with a sample JSON response.

#### Scenario: Agent author follows USAGE.md
- **WHEN** a reader runs the documented `/chat/ask` curl example against a live session
- **THEN** the command succeeds as written and the response matches the documented sample shape

### Requirement: Headless execution serves chat
A headless `-exec` run (without `-interactive`) SHALL start the status server, print its URL, and record the session so `-chat` and `-link` can reach it, and SHALL stream its execution status to that server. A server bind failure SHALL NOT fail the run: the run SHALL proceed without a server after reporting the bind error. The session record SHALL be cleared and the server shut down when the run ends.

#### Scenario: Chatting with an unattended run
- **WHEN** `determined -exec` is running headless and an agent runs `determined -chat -m "status"` on the same machine
- **THEN** the agent receives a status reply describing the execution in progress

#### Scenario: Bind failure degrades gracefully
- **WHEN** headless `-exec` cannot bind a listening port
- **THEN** the error is reported and the execute loop still runs to completion with the existing exit-code behavior

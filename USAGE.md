# Agent chat usage

`determined` exposes a read-only, machine-readable view of the current session.
The answers come directly from the live status snapshot; no AI tool is invoked
to compose them.

## Start and discover a session

Interactive planning and execution sessions serve chat. Headless execution now
does too:

```bash
determined -exec
# determined: status page at http://localhost:63431/
```

The latest session records its process and port in
`~/.determined/session.json`. Do not trust that file directly. `-link` verifies
the recorded process is alive, the port is listening, and the page identifies
itself as determined:

```bash
determined -link
# http://localhost:63431/
```

Discovery is machine-local and currently tracks one session. A stale or
unverifiable record is deleted and reported as `no running interactive session
found`.

## CLI modes

One-shot mode sends one request, prints the human-readable answer, closes, and
exits:

```bash
determined -chat -m "what is the status of this run?"
# The session is execution running after 2m0s; 3 of 5 steps are complete.
```

Persistent mode subscribes to live events and sends each non-empty stdin line
as a question:

```bash
determined -chat
determined chat — ask about status, plan, progress, activity, or logs
show progress
3 of 5 steps are complete:
[x] add protocol models
[x] add websocket transport
[x] mount chat endpoints
[ ] add client mode
[ ] document usage
[event:step] verifying step 4
```

EOF, an interrupt, or a server close ends persistent mode cleanly. Chat exits
`0` after a reply or clean close, `1` when discovery/transport/timeout fails,
and `2` for invalid flags. `-m` requires `-chat`; `-chat` cannot be combined
with `-plan`, `-exec`, `-review-plan`, `-criteria`, or `-interactive`.

## WebSocket protocol

Connect to `ws://localhost:<port>/chat` using RFC 6455. Messages are
unfragmented JSON text frames. Client frames must be masked. Frames over 1 MiB
close with code `1009`; fragmented or binary frames close with `1003`.
Ping/pong and a normal close handshake are supported.

Client request:

```json
{"id":"1","type":"message","text":"what is the status of this run?"}
```

Correlated reply:

```json
{"id":"1","type":"reply","text":"The session is execution running after 2m0s; 3 of 5 steps are complete.","data":{"intent":"status","phase":"execution running","elapsedSeconds":120,"currentActivity":"verifying step 4","progress":{"done":3,"total":5}}}
```

Subscribe once to receive changes until disconnect:

```json
{"id":"events","type":"subscribe"}
```

Example pushed event (events intentionally have no `id`):

```json
{"type":"event","event":"log","text":"verifying step 4","data":{"intent":"log","lastLog":{"at":"2026-07-21T10:02:00Z","message":"verifying step 4","body":"running go test ./...","state":"running"}}}
```

Unknown request types and malformed JSON receive an error without closing the
connection:

```json
{"id":"9","type":"error","error":"unknown request type"}
```

### Fields

| Direction | Field | Meaning |
|---|---|---|
| request | `id` | Caller-selected correlation ID. Replies copy it; events omit it. |
| request | `type` | `message` or `subscribe`. |
| request | `text` | Required question text for `message`. |
| response | `type` | `reply`, `event`, or `error`. |
| response | `text` | Human-readable answer or event summary. |
| response | `event` | For pushed events: `phase`, `step`, or `log`. |
| response | `error` | Validation/protocol error text. |
| response | `data` | Typed machine-readable values used to produce `text`. |

`data.intent` is one of `status`, `plan`, `steps`, `activity`, `log`, or
`help`. Depending on intent, data includes phase and elapsed seconds, step
counts/checklist, goal and plan markdown, the latest activity, a bounded log
tail, stop reason/advice, or the supported intent list. Unmatched questions
fall back to status and include the complete status snapshot in
`data.snapshot`.

## Plain HTTP and curl

Use the curl-friendly endpoint when a persistent WebSocket is unnecessary:

```bash
curl -s -X POST http://localhost:63431/chat/ask \
  -d '{"text":"what is the status of this run?"}'
```

Sample response:

```json
{"type":"reply","text":"The session is execution running after 2m0s; 3 of 5 steps are complete.","data":{"intent":"status","phase":"execution running","elapsedSeconds":120,"progress":{"done":3,"total":5}}}
```

Follow the existing full-snapshot SSE feed:

```bash
curl -N http://localhost:63431/events
```

Sample event:

```text
data: {"goal":"Add agent chat","phase":"succeeded","taskSteps":[{"text":"add protocol models","completed":true}],"execPhase":"running","execStopReason":"","execAdvice":""}
```

Check the WebSocket handshake with standard HTTP/1.1 curl (the one-second
timeout is expected because a successful upgrade keeps the socket open):

```bash
curl -i --http1.1 --max-time 1 \
  -H 'Connection: Upgrade' \
  -H 'Upgrade: websocket' \
  -H 'Sec-WebSocket-Version: 13' \
  -H 'Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==' \
  http://localhost:63431/chat
```

The response begins:

```text
HTTP/1.1 101 Switching Protocols
Upgrade: websocket
Connection: Upgrade
Sec-WebSocket-Accept: s3pPLMBiTxaQ9kYGzzhZRbK+xOo=
```

On curl builds with WebSocket URL support,
`curl --include ws://localhost:63431/chat` can perform the upgrade, but a
WebSocket client is still needed to mask and exchange the JSON frames reliably.

# Tasks: add-agent-chat-interface

## 1. Chat protocol models

- [ ] 1.1 Add `src/models/chat.go`: `ChatRequest` (`ID`, `Type` = `message`|`subscribe`, `Text`), `ChatResponse` (`ID`, `Type` = `reply`|`event`|`error`, `Text`, `Event`, `Error`, `Data`), typed `ChatIntent` constants (status, plan, steps, activity, log, help), and validation helpers with unit tests in `src/models/chat_test.go`

## 2. Minimal WebSocket transport (stdlib only)

- [ ] 2.1 Add `src/clients/websocket.go`: RFC 6455 server-side upgrade (validate headers, compute `Sec-WebSocket-Accept`, hijack the connection) and frame codec — unfragmented text frames, client masking, ping/pong, close handshake; reject fragmented/binary frames with close 1003 and frames over 1 MiB with close 1009
- [ ] 2.2 Add client-side dial support in the same file: HTTP upgrade request with random key, accept-key verification, masked writes, unmasked reads
- [ ] 2.3 Byte-level tests in `tests/websocket_test.go`: handshake accept value against the RFC example key, mask/unmask round trip, close-code behavior for binary, fragmented, and oversized frames, ping answered with pong

## 3. Chat service (deterministic answers)

- [ ] 3.1 Add `src/services/chat_service.go`: `ChatStatusSource` interface (`Snapshot()`, `Subscribe()`), `ChatService.Answer(ChatRequest) ChatResponse` implementing the intent table with status as default and fallback, replies carrying human-readable text plus structured data (phase, elapsed via clock, steps done/total, stop reason/advice, bounded log tail)
- [ ] 3.2 Tests in `src/services/chat_service_test.go` with a fake status source: each intent, fallback with full snapshot in data, unknown request type yields `error` with matching `id`, planning-phase vs executing-phase vs failed-run answers

## 4. Server endpoint

- [ ] 4.1 Mount `/chat` on `PlanStatusServer` (`src/clients/plan_status_server.go`): upgrade, then a read loop dispatching to an injected chat responder; `subscribe` attaches the status subscription and forwards snapshot-diff events (new step, new log entry, phase change) as `event` frames; malformed JSON answered with an `error` frame; write deadline per send so a stalled client cannot block
- [ ] 4.2 Tests in `tests/plan_status_server_chat_test.go`: end-to-end over a real listener — upgrade, message/reply correlation, subscribe then trigger a status change and observe the event frame, malformed payload keeps the connection open

## 5. Chat client mode

- [ ] 5.1 Add `src/services/chat_client.go` (session resolution via `SessionLocator` → `ws://localhost:<port>/chat`, one-shot send/await-with-deadline, interactive loop over injected reader/writer) keeping I/O behind interfaces for fakes
- [ ] 5.2 Wire flags in `cmd/determined/main.go`: `-chat` and `-m`, validation (`-m` requires `-chat`; `-chat` excludes `-plan`/`-exec`/`-review-plan`/`-criteria`/`-interactive`), exit codes 0/1/2 per spec, stderr messaging reusing `ErrNoSession`
- [ ] 5.3 Tests: flag validation table in `cmd/determined` tests; one-shot and interactive flows in `tests/chat_client_test.go` against an in-process chat server, including reply timeout → exit 1 and server close → exit 0

## 6. Headless -exec serves chat

- [ ] 6.1 In `main.go`, give the plain `runLoop` path a `PlanStatusService` + `PlanStatusServer` + session record (mirroring `runInteractiveExec` startup, without the feedback/hold loop): print URL lines, pass the service as the status reporter, shut down and forget the record when the run ends
- [ ] 6.2 Degrade on bind failure: report to stderr, run with a nil reporter as today; test that outcome and exit codes are unchanged when the server cannot start

## 7. Documentation

- [ ] 7.1 README: `-chat` section with the persistent and one-shot (`-chat -m "..."`) examples, note that headless `-exec` now serves the status page/chat endpoint
- [ ] 7.2 EXECUTION.md: chat exit codes, discovery behavior shared with `-link`, and the protocol sketch (message/reply/event JSON) for agent authors

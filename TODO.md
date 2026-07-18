# Chat Container on Status Page Right Pane

## Context

`determined` serves an embedded single-page status UI (`src/clients/plan_status_page.html`) over loopback while driving an AI coding tool (claude/droid/pi subprocess) through planning and execution phases. The page has a two-pane grid: tabs (Goal/Plan/Tests/Steps/Log/Execution) on the left, an Activity `<aside>` on the right. Users can annotate sections, but there is no way to *ask questions* about the plan, goal, steps, or tests. This change adds a chat widget to the right pane: user submits a question, it queues through the status service, the orchestrator stages it to `CHAT_QUESTION.md`, invokes the tool with a read-only Q&A prompt that writes its answer to `CHAT_RESPONSE.md`, and the answer streams back to the page via the existing full-snapshot SSE channel.

Decisions confirmed with user:
- **Availability:** answers arrive while planning waits for feedback or after execution finishes (chat drains on the `ServeFeedback`/`ServeAnnotations` goroutine only — never mid-execution, never concurrent with another tool run). Questions asked mid-run queue with a "Thinking…" indicator.
- **Scope:** strictly read-only Q&A. Prompt forbids modifying plan artifacts; change requests are redirected to the existing Annotate feature.

The design mirrors the existing annotation round-trip (`/annotate` → `SubmitAnnotation`/`TakeAnnotation`/`AnnotationSignal` → `applyAnnotation` staging `ANNOTATION.md`) — the closest existing template.

## Steps

### 1. Model: `ChatMessage` + snapshot fields
`src/models/plan_session_status.go`
- `ChatRole` string type with `ChatRoleUser`/`ChatRoleAssistant` consts.
- `ChatMessage { At time.Time; Role ChatRole; Text string }` (json: `at`, `role`, `text`).
- `PlanSessionStatus` gains `ChatMessages []ChatMessage` (json `chatMessages`) and `ChatPending bool` (json `chatPending`).
- Copy-on-write helper `WithChatMessage(msg)` following the `WithStep`/`WithLogEntry` pattern (~line 159).
- Test copy semantics in the existing model test file.

### 2. Config + prompt
`src/models/plan_config.go`, `src/services/planning_prompts.go`
- `PlanConfig` gains `ChatInvocation Invocation`, `ChatQuestionFile string`, `ChatResponseFile string`.
- `PlanningPrompts` struct gains `Chat string`; new `chatProtocol` const styled after `annotateProtocol` (line 18): read `CHAT_QUESTION.md`, read GOAL/PLAN/STEPS/TESTS/ANSWERS for context, write markdown answer **only** to `CHAT_RESPONSE.md`, do NOT modify any other file, do not create STOP.md; if question requests a change, redirect user to the Annotate button.
- Cover in `planning_prompts_test.go`.

### 3. Service: chat queue trio
`src/services/plan_status_service.go` — mirror annotation trio (lines 155-188):
- Fields: `chat chan struct{}` (cap 1), `pendingChat []string` under `mu`.
- `SubmitChatQuestion(q)`: via `update`, append user `ChatMessage` (clock-stamped), set `ChatPending = true`; push queue; non-blocking signal.
- `TakeChatQuestion() (string, bool)`: FIFO pop.
- `ChatSignal() <-chan struct{}`.
- `AddChatAnswer(text)`: via `update`, append assistant message; `ChatPending = len(pendingChat) > 0`.
- Tests in `tests/plan_status_service_test.go`: broadcast contains user message + pending flag; FIFO; answer clears pending; one signal per burst.

### 4. Server: `/chat` route + `ChatSink`
`src/clients/plan_status_server.go`
- `type ChatSink interface { SubmitChatQuestion(question string) }` next to `AnnotationSink` (~line 28).
- Constructor gains 5th arg: `NewPlanStatusServer(source, annotations, implement, chat, clock)`.
- Register `mux.HandleFunc("/chat", s.serveChat)` (~line 66-71); handler mirrors `serveAnnotate` (~141-158): POST-only (405 otherwise), decode `{"question": string}`, 400 on decode error or blank question, else submit + 202.
- **Ripple (same commit):** `cmd/determined/main.go:375` → `NewPlanStatusServer(status, status, status, status, clock)`; all constructor calls in `tests/plan_status_server_test.go` gain a `fakeChatSink{questions []string}`.
- Server tests: valid question 202 + recorded on fake sink; GET 405; blank/bad JSON 400.

### 5. Orchestrator: answer questions
`src/services/plan_orchestrator.go`
- `PlanStatusReporter` interface (~33-47) gains `TakeChatQuestion() (string, bool)`, `ChatSignal() <-chan struct{}`, `AddChatAnswer(string)` (service already satisfies after step 3).
- Add `case <-o.status.ChatSignal(): o.drainChat(ctx)` to both `ServeAnnotations` (~534) and `ServeFeedback` (~555) selects; call `drainChat` alongside `drainAnnotations` at loop entry so mid-run questions answer on return.
- `drainChat(ctx)`: loop `TakeChatQuestion` until empty, calling `answerChat`.
- `answerChat(ctx, question)`, mirroring `applyAnnotation` (~590):
  1. `files.Write(cfg.ChatQuestionFile, chatQuestionDocument(question))` (small `# Question\n\n<text>` renderer like `annotationDocument`).
  2. `runInvocation(ctx, cfg.ChatInvocation, "answering chat question")` — reuses retry/failure accounting; run shows in Log tab.
  3. Read `cfg.ChatResponseFile`; success → `AddChatAnswer(content)`; missing/unreadable → `AddChatAnswer("The tool did not produce an answer; please try again.")` so pending always clears.
  4. Remove both files. Do not republish artifacts (chat must not touch them).
- Update `fakeStatusReporter` in `src/services/plan_status_reporting_test.go` with the three methods.
- Orchestrator tests (pattern: `src/services/plan_annotation_test.go`): one chat invocation per question; question file written then removed; response content recorded; missing-response fallback; chat + annotation signals both drain without interleaved invocations.

### 6. main.go wiring
`cmd/determined/main.go` — in `runPlan` PlanConfig literal (~333-352): `ChatInvocation: tool.Invocation(prompts.Chat)`, `ChatQuestionFile: "CHAT_QUESTION.md"`, `ChatResponseFile: "CHAT_RESPONSE.md"`. Constructor update at line 375. `runReviewPlan` has no status server — no changes.

### 7. HTML/JS chat UI
`src/clients/plan_status_page.html`
- New `<section id="chat">` inside `<aside id="activity">` (~251-258): heading, `<div id="chat-messages">`, hidden `#chat-thinking` ("Thinking…"), textarea + Send button reusing `.annotate-form`/`.send` visual language. CSS: `.chat-msg.user` / `.chat-msg.assistant` bubbles; `#chat-messages { max-height: 40vh; overflow-y: auto; }`.
- Send handler mirrors annotate fetch (~654-665): trim, POST `/chat` JSON, clear textarea; Enter sends, Shift+Enter newline; input stays enabled while pending (questions queue server-side).
- `renderChat(status)` called from `render()` (~702): rebuild from `status.chatMessages`; assistant text through existing `markdownToHtml` (~489) with `looksLikeMarkdown` guard, user text via `textContent`; per-message `<time>`; toggle thinking on `status.chatPending`; auto-scroll only when message count grew.
- Empty state: "Ask a question about this plan — answers are read-only; use Annotate to request changes."

### 8. Verify
- `go build ./... && go test ./...` (stdlib only, hand-rolled fakes).
- Manual: run interactive plan, ask question during feedback wait → user bubble instant, "Thinking…" shows, markdown answer renders, `CHAT_QUESTION.md`/`CHAT_RESPONSE.md` cleaned up, plan artifacts untouched. Ask question mid-execution → queues, answers when run returns. Frontend check with agent-browser if available.

## Risks
- **Constructor/interface ripple:** `NewPlanStatusServer` 5th arg and `PlanStatusReporter` growth break build until main.go + test fakes updated — do in same commit as steps 4/5.
- **Tool disobedience:** tool could still edit PLAN.md despite prompt; same trust level as other invocations. `tamper_guard.go` hardening is a follow-up.
- **Snapshot growth:** SSE carries full chat history each broadcast — fine at human volume, same trade-off as `Log`/`ExecLog`.

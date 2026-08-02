# NOTES

## Step: Require `## Assumptions` section in plan prompt (done 2026-08-02)
- Requirement text appended to `planProtocol` in `src/services/planning_prompts.go`,
  so all three modes (standard/MVP/prototype) inherit it — no per-mode duplication.
- Exact prompt wording: "PLAN.md must record every assumption and chosen default
  under a `## Assumptions` heading, one markdown list item per assumption,
  instead of burying them in prose."
- Format contract for later parsing step: heading is exactly `## Assumptions`,
  body is a markdown list, one item per assumption. Heading is NOT mandated when
  no assumptions exist — prompt only requires recording assumptions that were
  made, so `Assumptions()` extractor must treat missing heading as "" (matches
  the skip-round behavior in the later orchestrator step).
- Test: `TestPlanRequiresAssumptionsSection` in
  `src/services/planning_prompts_test.go`, asserts substrings across all modes.

## Step: Extract assumptions section from PLAN.md (done 2026-08-02)
- `Assumptions() string` on `PlanDocumentPublisher` in
  `src/services/plan_document_publisher.go`. Reads `p.cfg.PlanFile`; any read
  error or missing `## Assumptions` heading returns "" — never an error.
- Heading match is exact `## Assumptions` (line trimmed of whitespace only,
  case-sensitive), per the format contract from the prompt step.
- Section body ends at the next `# ` or `## ` heading or end of file; `###`
  subheadings stay inside the body. Body is TrimSpace'd, so a heading with an
  empty body also returns "" — orchestrator skip-round check (`== ""`) covers
  both "no heading" and "heading, no items".
- Helpers `assumptionsSection` / `assumptionsStart` / `sectionHeading` are
  package-private in the same file; reuse them if later steps parse other
  PLAN.md sections.
- Tests in `src/services/plan_document_publisher_test.go`:
  section body, section-at-EOF, old-format plan without heading, missing
  PLAN.md.

## Step: Render assumptions block on status page (done 2026-08-02)
- `Assumptions string` added to `models.PlanSessionStatus` (json `assumptions`),
  `SetAssumptions(string)` on both `PlanStatusReporter` (plan_orchestrator.go)
  and `PlanStatusService`.
- Published from `reportPlan` only: after `docs.PublishPlan`, it calls
  `o.status.SetAssumptions(o.docs.Assumptions())`. Nil reporter path unchanged
  (reportPlan early-returns). NOT part of `PlanDocumentSink` — the publisher
  still only publishes files; assumptions extraction stays orchestrator-driven.
- Every reportPlan now emits an extra "assumptions" event on
  `fakeStatusReporter`; the exact-sequence test in
  plan_status_reporting_test.go was updated. Future steps adding reportPlan
  calls must expect the trailing "assumptions" event.
- Page: `#plan-assumptions` block (h3 "Assumptions" + `#plan-assumptions-doc`)
  sits inside `#plan-body` ABOVE the plan doc; hidden when assumptions == "".
  `renderAssumptions(status.assumptions)` called from `render()`.
- Tests: JS block-render test in tests/plan_status_page_test.js, broadcast
  test in tests/plan_status_service_test.go. Nil-reporter coverage is the
  pre-existing `TestPlanWithoutReporterRunsTerminalOnly`.

## Step: Assumption confirmation round after drafting (done 2026-08-02)
- `confirmAssumptions(ctx)` in `src/services/plan_orchestrator.go`, called in
  BOTH `create()` branches that reach `refine` (pre-drafted plan at loop top,
  and freshly drafted plan), after `ensureTests`. NOT called on the review
  path — review has its own question relay; add it there explicitly if a
  later step wants it.
- Skip check is `o.docs.Assumptions() == ""` (covers missing heading and
  empty body, per the extractor contract). No `Ask`, no progress line.
- One `Prompter.Ask` per round. Confirm answers (after TrimSpace+lowercase):
  `""`, `y`, `yes`, `ok` — helper `assumptionsConfirmed(string)`. Anything
  else is a correction; there is no re-ask loop. `Ask` error returns
  `OutcomeInterrupted` like the other prompt sites.
- A correction reuses `applyAnnotation` with
  `{Section: AnnotationSectionPlan, Target: "Assumptions", Comment: answer}`,
  so it stages ANNOTATION.md, runs `AnnotateInvocation`, removes the file,
  and republishes via `reportPlan` (which also re-emits the "assumptions"
  status event). Because the section is plan, `refreshDemo` also runs — a
  no-op when `DemoInvocation.Binary` or `DemoFile` is empty (true in tests).
- Round emits `writeProgress` "confirming plan assumptions" and
  `status.WaitForInput()` before asking; tests asserting exact status-event
  sequences must account for these when PLAN.md carries a `## Assumptions`
  section. Existing sequence tests use plans without the section, so they
  were untouched.
- Tests at bottom of `plan_orchestrator_test.go`:
  `TestPlanAssumptionsConfirmationProceedsToRefinement` (Enter + `y`, next
  invocation is `assess`), `TestPlanAssumptionsCorrectionAnnotatesAndRepublishes`
  (annotate invocation sees the staged annotation; republish observed via
  `fakeStatusReporter.assumptions`/`plan` after the annotate call mutates
  PLAN.md), `TestPlanWithoutAssumptionsSectionSkipsConfirmation`. Shared
  fixture const `planWithAssumptions`.

## Step: Split create-mode assessment prompt into findings and questions (done 2026-08-02)
- `assessmentPrompt` (all three modes — split lives in the shared tail, no
  per-mode duplication): objective findings still go to REFINEMENTS.md
  ("write exactly NONE" contract unchanged); preference/intent/risk-dependent
  findings become numbered questions in QUESTIONS.md, with explicit
  "do not resolve preference-dependent findings yourself".
- File names are QUESTIONS.md / ANSWERS.md (create-mode planning files), NOT
  REVIEW_QUESTIONS.md / REVIEW_ANSWERS.md (review mode keeps its own). The
  refine-loop relay step must therefore read/write QUESTIONS.md and
  ANSWERS.md for create mode.
- `refinementPrompt` now opens with "Read GOAL.md, PLAN.md, STEPS.md,
  REFINEMENTS.md, and ANSWERS.md if it exists." and "Treat the user's
  answers in ANSWERS.md as authoritative." — mirrors ReviewPrompts().Refine.
- No orchestrator change here; create-mode assess can now write QUESTIONS.md
  but nothing relays it until the next step drops the `PlanOperationReview`
  guard in `refine`.
- Tests: `TestAssessmentSplitsFindingsFromPreferenceQuestions` and
  `TestRefinementReadsUserAnswers` in planning_prompts_test.go, asserted
  across all three modes.

## Step: Relay assessor questions in create-mode refine loop (done 2026-08-02)
- One-line change in `refine` (`src/services/plan_orchestrator.go`): dropped
  `o.cfg.Operation == models.PlanOperationReview &&` from the QUESTIONS.md
  guard. Both modes now relay via the shared `relayQuestions` before the
  refine invocation; file names come from cfg (`QuestionsFile`/`AnswersFile`),
  so create mode uses QUESTIONS.md/ANSWERS.md and review keeps
  REVIEW_QUESTIONS.md/REVIEW_ANSWERS.md — no new code paths.
- Relay runs before the `pass >= MaxRefinePasses` check, so questions are
  still asked and recorded on the final pass even though no refine follows.
  Existing behavior for review mode, now shared.
- `relayQuestions` clears QUESTIONS.md after appending the round to
  ANSWERS.md, so the refine invocation sees ANSWERS.md only. Progress line in
  create mode is "answering planning questions" (`questionProgress`).
- Test: `TestPlanRefineRelaysAssessorQuestionsInCreateMode` (bottom of
  plan_orchestrator_test.go). Captures fs state inside the fake runner's
  refine call (call 3) to prove ANSWERS.md written and QUESTIONS.md cleared
  before refine; asserts invocation prompts "assess" then "refine" via
  `fakeRunner.prompt`.
- Gotcha for later steps: any create-mode refinement test whose assessment
  script writes QUESTIONS.md now triggers a Prompter.Ask — fakePrompter with
  no answers returns io.EOF and the run ends OutcomeInterrupted. Existing
  tests were unaffected (none wrote QUESTIONS.md during assess in create
  mode).

## Step: Gate refine-cap exhaustion on explicit user choice (done 2026-08-02)
- Pass-cap branch in `refine` (`src/services/plan_orchestrator.go`) replaced by
  `gateExhaustedCap(issues, pass)` returning `(models.Outcome, refineCapAction)`:
  `capReturn` (return the outcome), `capReassess` (continue loop, fresh assess,
  no refine invocation), `capRefineMore` (fall through to one refine pass).
- Gate flow: `publishRemainingFindings` prints each finding to the terminal
  AND publishes each via `notifyProgress(o.status, "unresolved finding: <f>")`
  — fake reporter records `progress: unresolved finding: ...` events. No new
  PlanStatusReporter method was added; findings ride the existing Progress
  sink. If a later step wants a dedicated status-page block, add
  `SetFindings` then.
- `askCapChoice` emits `WaitForInput` (status non-nil), then loops
  `Prompter.Ask(refineCapQuestion)` until `parseCapChoice` (trim+lowercase;
  exact words `accept`/`refine`/`edit`) matches; invalid answers re-ask same
  question. Ask error = the Stop path and returns `capChoiceStop`, which the
  gate maps to `OutcomePlanStalled` (NOT OutcomeInterrupted like other
  prompt sites — spec said Stop stalls the plan). Status-page Stop during the
  granted refine/assess invocations still yields OutcomeUserStopped via
  runInvocation, unchanged.
- `refine` chosen: exactly one more pass because `pass` only grows, so
  `pass >= MaxRefinePasses` stays true and a still-dirty assessment re-enters
  the gate with the identical question. No counter, no auto-accept.
- `edit` (`awaitPlanEdits`): one Ask "Press Enter when you have finished
  editing <PlanFile> and <StepsFile>"; any answer (incl. empty) proceeds,
  Ask error stalls. Removes AssessmentFile, `continue` → re-assess. It does
  NOT reportPlan the manual edits before assessing; the final
  reportFinish republish covers status.
- `accept` removes AssessmentFile and returns OutcomePlanReady.
- Gotcha: any test that exhausts the cap with findings now blocks on the
  prompter; empty fakePrompter (io.EOF) means OutcomePlanStalled where the old
  code returned OutcomePlanReady. Old `TestPlanRefinementStopsAtPassCap` was
  replaced by the `TestExhaustedCap*` suite (shared `exhaustedCapFixture`,
  cap=1) in plan_orchestrator_test.go covering accept/refine/edit, findings
  on fakeStatusReporter, dirty-pass re-ask, invalid-answer re-ask, and
  EOF-stall (no silent accept path).
- Review mode shares `refine`, so review runs also gate at cap exhaustion now
  (accept maps to OutcomePlanReviewed via review()'s translation).

## Step: Parse misaligned verdicts and notes from TESTS.md (done 2026-08-02)
- `MisalignedTests() []MisalignedTest` on `TestsDocument`
  (src/services/tests_document.go). `MisalignedTest{Heading, Note string}` —
  Heading is the raw `### Test N: ...` line (same convention as the other
  TestsDocument methods), Note is the trimmed `**Alignment note:**` text or
  "" when the line is absent. No error path: verdict-free/aligned/partial
  sections are skipped silently.
- Verdict match reuses `alignmentLinePattern` (case-insensitive, so
  `MISALIGNED` counts); note comes from new `alignmentNotePattern`
  (`**Alignment note:**` to end of line). Note line placement within the
  section does not matter — first match wins.
- Later gate step: an empty Note means the assessor gave no reason; render
  the heading alone rather than an empty bullet.
- Tests appended to src/services/tests_document_test.go
  (TestMisaligned*/TestAlignedAndPartial*/TestVerdictFree*).

## Step: Plumb the realign prompt and invocation (done 2026-08-02)
- `realignProtocol` const in src/services/planning_prompts.go, exposed as
  `Realign` on `models.PlanningPrompts` from both `PlanningPrompts(mode)` and
  `ReviewPrompts()`. Prompt: rewrite only sections carrying
  `**Alignment:** misaligned`, replace in place keeping the `### Test N`
  heading number and format, re-embed `alignmentRequirement` (new tests get
  fresh verdicts), keep aligned/partial tests and everything else verbatim,
  no PLAN.md/STEPS.md changes, no STOP.md.
- `RealignInvocation` field on `models.PlanConfig` (after AlignInvocation).
  Wired as `tool.Invocation(prompts.Realign)` in `createPlanConfig` and in
  new `reviewPlanConfig` in cmd/determined/main.go — review config was
  extracted out of `runReviewPlan` into `reviewPlanConfig(tool, budget,
  maxStepPasses, maxFailures)` so the cmd test can assert the assembled
  review config; `runReviewPlan` behavior unchanged.
- Tests: `TestRealignPromptReplacesOnlyMisalignedTests`
  (planning_prompts_test.go, both constructors) and
  `TestAssembledPlanConfigsCarryRealignInvocation` (main_test.go, package
  main so it calls createPlanConfig/reviewPlanConfig directly; asserts
  non-empty Binary+Args and the prompt text riding in Args).
- Gotcha for the ensureAlignment step: orchestrator fixtures build PlanConfig
  by hand (plan_orchestrator_test.go:75 sets AlignInvocation only) — set
  RealignInvocation there too when the gate starts running it, else the
  fake runner sees an empty invocation.

## Step: Run the automatic realign pass in ensureAlignment (done 2026-08-02)
- `realignMisaligned(ctx)` in src/services/plan_orchestrator.go, called from
  BOTH exits of `ensureAlignment` that previously returned
  `OutcomePlanReady` (verdicts already present, and verdicts appearing after
  the align invocation) — so pre-judged TESTS.md with misaligned tests also
  realigns.
- Flow: `misalignedTests()` (reads TestsFile, `MisalignedTests()`); if
  non-empty, exactly one `runInvocation(RealignInvocation, "realigning
  tests")`, then re-parse. Still-misaligned after the rewrite does NOT stall
  and does NOT ask: prints "N test(s) in TESTS.md remain misaligned after
  the rewrite" and proceeds `OutcomePlanReady`. The later gate step should
  hook in right after that re-parse (the second `misalignedTests()` result
  is already in hand inside `realignMisaligned`).
- Read errors before/after the invocation stall (`OutcomePlanStalled`),
  matching the ensureAlignment convention.
- Fixture: `planConfig` in plan_orchestrator_test.go now sets
  `RealignInvocation` (`-p realign`) — prompt-arg assertions key off
  `Args[1] == "realign"`.
- Tests: TestPlanRealignsMisalignedTestsOnceWithoutAsking (misaligned then
  partial rewrite; 2 runner calls, realign second, zero Prompter.Ask),
  TestPlanRealignsOnlyOnceWhenTestsStayMisaligned (still proceeds, terminal
  notice, no second realign), TestPlanSkipsRealignWhenNoTestIsMisaligned.
- Gotcha: any orchestrator test whose TESTS.md carries a `misaligned`
  verdict now triggers an extra runner call; keep fake scripts' call counts
  in mind.

## Step: Gate remaining misaligned tests on explicit user choice (done 2026-08-02)
- `gateMisaligned(ctx)` in src/services/plan_orchestrator.go, called from
  `realignMisaligned` right after the automatic realign invocation (the old
  "proceed with a terminal notice" tail is gone). Loop: re-parse verdicts →
  none misaligned = OutcomePlanReady → else `publishMisalignedTests` +
  `askAlignChoice`.
- Publishing: terminal header "N test(s) in TESTS.md remain misaligned after
  the rewrite:" plus one `  - <heading> — <note>` line per test (heading alone
  when the note is empty), each also sent via
  `notifyProgress(status, "misaligned test: <line>")` — same Progress-sink
  ride as the refine-cap findings; no new reporter method.
- `askAlignChoice` mirrors `askCapChoice`: WaitForInput, loop on invalid
  answers ("please answer accept, rewrite, or drop"), Ask error =
  `alignChoiceStop` which the gate maps to OutcomePlanStalled (matching the
  missing-verdict stall, NOT OutcomeInterrupted). `parseAlignChoice`
  trims+lowercases exact words accept/rewrite/drop.
- `rewrite` reruns `RealignInvocation` ("realigning tests"); `drop` runs
  `constrainedInvocation(RealignInvocation, dropTestsConstraint)`
  ("dropping misaligned tests"). Both re-enter the loop, so a still-dirty
  TESTS.md re-asks; the ONLY exits to OutcomePlanReady are a clean re-parse
  or an explicit accept.
- `constrainedInvocation` (package-private, plan_orchestrator.go) copies
  Args and appends "\n\n<instruction>" to Args[1] — every supported Tool
  (droid/pi/claude) places the prompt at Args[1]; if a new Tool breaks that
  contract, this helper must change. Reuse it for future one-off prompt
  constraints instead of plumbing new PlanConfig invocations.
- `dropTestsConstraint` text: delete each `**Alignment:** misaligned`
  section entirely (heading/narrative/diagram/verdict lines), keep everything
  else verbatim, add no new tests. Tests assert the marker substring
  "delete that entire section".
- Status-page Stop during a granted rewrite/drop invocation still returns
  OutcomeUserStopped via runInvocation (stop=true propagates), unchanged.
- Tests: `TestPlanRealignsOnlyOnceWhenTestsStayMisaligned` was REPLACED (its
  no-ask/proceed behavior no longer exists) by the `TestMisalignedGate*`
  suite: accept (publishes finding + wait-for-input on fakeStatusReporter,
  no third invocation), rewrite (prompt(3)=="realign", clean re-check asks
  once), drop (constrained prompt + section removal, fixtures
  stubbornTestsDoc/droppedTestsDoc), re-ask while dirty (rewrite,rewrite,
  accept = 3 asks, 4 runner calls), invalid-answer re-ask, and EOF-stall
  (OutcomePlanStalled, no silent accept). Shared `misalignedGateFixture`
  (calls 1-2 write stubbornTestsDoc; `afterGate` scripts calls 3+; nil
  afterGate keeps TESTS.md stubborn forever).
- New shared test helper `containsEvent(events, want)` in
  plan_orchestrator_test.go — use it instead of hand-rolled loops over
  fakeStatusReporter.events.
- Gotcha: any test whose TESTS.md still carries a misaligned verdict after
  the automatic realign now blocks on the prompter; empty fakePrompter means
  OutcomePlanStalled where pre-gate code returned OutcomePlanReady.

## Step: Require explicit approval before chained execution (done 2026-08-02)
- `approveExecution(prompter services.Prompter, status inputWaiter) bool` in
  cmd/determined/plan_flow.go. `inputWaiter` is a new one-method interface
  (`WaitForInput()`) defined in plan_flow.go so the headless call site can
  pass nil — ExecStatusReporter does NOT have WaitForInput, only
  PlanStatusService/PlanStatusReporter do, hence the narrow local interface.
- Only trimmed, lowercased `y`/`yes` approve; empty answer, anything else,
  and Ask errors (EOF) all decline. Decline returns the caller's existing
  OutcomePlanReady and touches no files.
- Call sites: main.go headless chain — `shouldExecuteAfterPlan(...) &&
  approveExecution(clients.NewStdinPrompter(os.Stdout, os.Stdin), nil)`
  (nil waiter: no status page exists yet at that point; runHeadlessExec
  creates its own later). Interactive `postPlanAutoExec` in
  runInteractivePlan — declines return `outcome` before the "executing now"
  banner / runAutoExec; status (PlanStatusService) is the waiter, so the
  page shows "waiting for input on the terminal".
- Gotcha: `-plan X -exec` (headless) now blocks on stdin after planning.
  Any script/e2e driving that path must feed `y\n` or expect exit with
  OutcomePlanReady.
- Test fakes added in cmd/determined/main_test.go: `fakePrompter`
  (answer/err, records asked questions) and `fakeInputWaiter` — reuse them
  for later cmd-level prompting steps. `chainedExecution` helper in the test
  file mirrors both call-site guards.

## Step: Prove old-format artifacts and nil reporters keep working (done 2026-08-02)
- Four regression tests in new file
  src/services/plan_gates_regression_test.go (package services_test, reuses
  the existing fakes and fixtures — validTestsDoc, stubbornTestsDoc,
  planWithAssumptions, planConfig):
  - TestOldFormatPlanWithoutAssumptionsSkipsConfirmationRound: resume path —
    PLAN.md/STEPS.md/TESTS.md pre-seeded on disk (unlike the existing
    tool-drafted TestPlanWithoutAssumptionsSectionSkipsConfirmation), cap=1,
    empty prompter proves zero Asks straight into assess.
  - TestRefineWithoutQuestionsFileRelaysNothing: dirty assess with no
    QUESTIONS.md, cap=2; asserts zero Asks AND ANSWERS.md never created.
  - TestOldFormatTestsWithoutVerdictsKeepAlignmentBehavior: verdict-free
    TESTS.md pre-seeded; exactly one align invocation, no realign, no Asks.
  - TestNewGatesRunWithNilStatusReporter: ONE run fires all four in-orchestrator
    gates in order — misaligned gate (accept), assumptions confirm (Enter),
    question relay (answer), exhausted-cap gate (accept) — with no
    WithStatusReporter call; proves nil-guard on every WaitForInput /
    notifyProgress path. Gate order in create(): ensureTests/alignment gates
    run BEFORE confirmAssumptions, then refine gates.
- Exec-approval gate's nil case was already covered at cmd level (nil
  inputWaiter in plan_flow.go tests), so it is not re-proven here.
- make test is a docker build; a repeat run shows CACHED steps and exit 0 —
  that still means the suite ran green on the current sources.

## Step: Manual attended run exercising each gate (done 2026-08-02)
- Run performed end to end in /tmp/det-manual (sandbox, non-git so branch
  isolation no-ops): a fake `droid` shell script first on PATH dispatches on
  the prompt text at $2 of `droid exec "<prompt>" ...` (unique markers: "You
  are planning software", "Write only TESTS.md", "Assess each recommended
  test", "Rewrite each test whose", "Evaluate each step as a capable
  implementer", "Resolve every listed planning issue", "Read ANNOTATION.md",
  "DEMO.html"), with counter files making realign call 1 stubborn and assess
  call 1 dirty. Driven attended via /usr/bin/expect over a pty
  (drive.exp) — expect is REQUIRED here: piping answers into stdin fails
  because approveExecution builds a fresh StdinPrompter whose bufio reader
  finds only EOF after the orchestrator's prompter buffered the pipe ahead.
- Command: `determined -plan "..." -exec -interactive -max-step-passes 1
  -tool droid`. Gate order observed: misaligned gate → assumptions confirm →
  assessor-question relay → exhausted-cap gate → exec approval.
- All five gates asked and honored: rewrite → second realign left TESTS.md
  all-aligned; assumption correction → annotate invocation rewrote PLAN.md
  (port 8080 → 9999) and the page's assumptions field carried the corrected
  text afterward; relay answer recorded verbatim in ANSWERS.md and
  QUESTIONS.md cleared; cap `refine` ran exactly one more refine + clean
  re-assess to plan-ready; exec approval answered `n` — no execution
  invocation ran, plan files intact, outcome "plan ready".
- Status page observed via SSE: `curl -sN --max-time 2 <url>/events`
  captured at each gate. Evidence lives in the `steps` array (Progress sink),
  NOT `log` (log holds only invocation entries): "misaligned test: …",
  "unresolved finding: …", "waiting for input on the terminal";
  `waitingForInput` was true in every gate snapshot.
- Gotcha: the page's `assumptions` field is empty during the misaligned and
  assumptions gates — reportPlan first publishes it at refine start (and on
  the annotate republish). The confirm prompt itself shows the list on the
  terminal only. If the page should show assumptions before refine, add a
  reportPlan/SetAssumptions call ahead of confirmAssumptions.
- Artifacts (transcript.log, gate*.sse, protocol files) left in
  /tmp/det-manual/run for inspection; nothing committed. make test exit 0.

## Step: Close the unauthenticated network bypass of the exec approval gate (done 2026-08-02)
- Chose remediation 1 from FIXES.md: loopback bind by default, no token. The
  status server (src/clients/plan_status_server.go) now defaults its bind
  host to 127.0.0.1 (statusServerLoopbackHost); Start() listens on
  net.JoinHostPort(host, "0"). Remote exposure is an explicit opt-in via the
  new builder WithBindHost(host) — it sets the host unconditionally, no
  empty-string fallback.
- cmd wiring: new flag `-status-host` (default "127.0.0.1"); its value
  threads main() → runPlan → runInteractivePlan, and into runHeadlessExec /
  runInteractiveExec → startStatusSession, which chains
  .WithBindHost(statusHost) on the server builder. Anyone adding a new
  status-session entry point must pass the host through the same way.
- URL() still prints http://localhost:<port>/; doc comments updated to say
  remote substitution of the machine IP only works after the opt-in.
- cmd/hangrepro was NOT given the flag — it now serves on loopback only,
  which suits a local repro tool.
- Acceptance tests in tests/plan_status_server_bind_test.go:
  TestImplementUnreachableFromNonLoopbackByDefault (dial of a real
  non-loopback interface IP refused, sink at 0 requests; loopback POST
  /implement 202 and sink at 1) and
  TestImplementReachableAfterExplicitBindOptIn (WithBindHost("0.0.0.0"),
  POST via external IP 202). Helper nonLoopbackAddress skips when the
  machine has no non-loopback IPv4 (docker containers have eth0, so make
  test exercises both).
- Gotcha for any future remote-exposure work: with `-status-host 0.0.0.0`
  the pre-fix exposure returns — /implement (and /annotate, /task/*,
  /stall/choice, /chat/ask) accept any caller. A per-session token
  (FIXES.md remediation 2) is the follow-up if remote use becomes a real
  feature rather than an opt-in escape hatch.
- make test exit 0.

## Step: Defend the status server against browser-borne requests (done 2026-08-02)
- Both FIXES.md remediations implemented in src/clients/plan_status_server.go.
- Host allowlist: `hostGuard` wraps the whole mux (set in Start), 403 on any
  request whose Host is not localhost/127.0.0.1/::1 with the bound port (a
  port-less Host like bare `localhost` is allowed). Active only while the
  bind host is loopback (`loopbackName(s.host)`); a WithBindHost opt-in to a
  non-loopback interface disables it, since remote hostnames are then
  legitimate. `-status-host 0.0.0.0` therefore also turns the guard off.
- Origin check: serveChat rejects a present, non-local Origin with 403 before
  the upgrade; an ABSENT Origin is allowed — the Go chat dialer
  (services/chat_client.go via DialWebSocket) sends none. Same
  loopback-bind condition as the host guard. If the page ever opens the
  WebSocket itself, its Origin will be local and passes.
- Session token: 32 random bytes hex (64 chars), generated in Start()
  (rand.Read failure fails Start). Exposed via `SessionToken()` (valid only
  after Start) and injected into the served page by replacing the
  `{{SESSION_TOKEN}}` placeholder in the `session-token` meta tag; page JS
  reads it into `sessionHeaders` and sends header `X-Session-Token`
  (exported const `clients.StatusSessionTokenHeader`) on every
  state-changing fetch. Required — regardless of bind host — on /implement,
  /annotate, /task/skip, /task/stop, /stall/choice, /explain/start,
  /chat/ask. Check order in handlers: method (405) → token (403) → sink
  availability (503) → payload (400); tests key off that ordering.
  `authorized` fails closed when s.token is "" (Start not run).
- Gotcha for anyone curling /chat/ask (the "curl-friendly" endpoint): it now
  needs the token — pull it from the page meta tag or server.SessionToken().
- Test plumbing changes: tests/plan_status_server_test.go
  `startAnnotateServer` now returns the *server* (call sites use
  server.URL()/SessionToken()); shared helper `postWithToken` lives there and
  is reused by task_control_test.go's postStatus. startChatServer likewise
  returns the server (chat_client_test.go, websocket_test.go,
  plan_status_server_chat_test.go updated; `postChatAsk` helper).
  stall_choice_handler_test.go (package clients, direct handler calls) sets
  s.token by hand. The JS harness fake document gained `querySelector`
  returning a stub token for `meta[name="session-token"]` — any new page-JS
  top-level DOM lookups need the same treatment.
- Acceptance tests in tests/plan_status_server_browser_guard_test.go:
  foreign-Host 403 (GET and tokened POST /implement, sink untouched;
  wrong-port Host also 403; three local Host forms 200), tokenless POST 403
  across all seven endpoints with sinks untouched plus the page-embedded
  token flow reaching 202/sink=1 (`pageEmbeddedToken` regex-extracts from
  the served HTML), and raw-handshake Origin tests (foreign 403, local 101).
- make test exit 0.

## Step: Automate the TESTS.md Test 1 end-to-end journey (done 2026-08-02)
- Single test `TestPlanJourneyExercisesEveryGateAndDeclinesExecution` in new
  file cmd/determined/plan_journey_test.go (package main — it must call the
  unexported `shouldExecuteAfterPlan`/`approveExecution` via the existing
  `chainedExecution` mirror and reuse `fakePlanExecutor`/`fixedClock`).
- Package main cannot import the services test fakes, so the file carries
  journey-local duplicates (journeyFileStore/Prompter/Runner/LogSink/
  StatusReporter). journeyStatusReporter implements the full
  services.PlanStatusReporter; nil signal channels are safe because
  AnnotationSignal/ImplementSignal/ExplainSignal are only read in
  ServeAnnotations/ServeFeedback, never in Run.
- Journey sequence (MaxRefinePasses=2, drafted by the fake runner, 7 calls):
  plan(questions) → clarifying relay → plan(drafts PLAN with ## Assumptions +
  misaligned TESTS.md) → realign fixes verdict (no gate ask) → assumptions
  correction → annotate + republish → assess(2 findings + question) → relay →
  refine (captures ANSWERS.md/cleared QUESTIONS.md at call time) → assess
  still dirty → cap gate `accept` → chained exec approval bare Enter declines.
- Cap needs MaxRefinePasses=2 to get a real refine invocation between relay
  and exhaustion; cap=1 exhausts before any refine runs (see the regression
  test), which would defeat the "answer recorded before the refiner runs"
  assertion.
- Exec tail wired exactly like the headless call site:
  `if shouldExecuteAfterPlan(true, false, planOutcome) { chainedExecution(...) }`
  with the SAME journeyPrompter continuing its answer script — proves the
  approval is the 5th and last question of one continuous conversation.
- refineCapQuestion / assumptionsQuestion texts are unexported; the test
  matches distinctive substrings ("accept (finish with the plan as-is)",
  "The plan rests on these assumptions:") not full strings.
- make test exit 0 (docker build, CACHED steps fine per earlier note).

## Step: Forbid framing of the status page (done 2026-08-02)
- servePage (src/clients/plan_status_server.go) now sets
  `X-Frame-Options: DENY` and `Content-Security-Policy: frame-ancestors 'none'`
  on every page response (all paths routed through servePage: /, /goal, /plan,
  /tests, /tests/journey, /tests/bdd, /steps, /log, /exec, /explain — one
  handler, so one place to set headers).
- Headers are page-only: /events, /status, and the state-changing POST
  endpoints do not carry them (they serve JSON/SSE, not frameable documents).
  If a later step adds a new HTML-serving handler outside servePage, it must
  set both headers itself.
- CSP is exactly `frame-ancestors 'none'` — no other directives. Adding
  script-src/style-src later WILL break the page (inline <script>/<style>)
  unless 'unsafe-inline' or hashes are added; extend the value, do not replace.
- Direct-tab flow unaffected: frame-ancestors only restricts embedding
  contexts, not top-level navigation.
- Test: TestServedPageForbidsFraming in
  tests/plan_status_server_frame_test.go (reuses startAnnotateServer).
- make test exit 0.

## Step: Drop the wasted demo regeneration from the assumption-correction path (done 2026-08-02)
- applyAnnotation split: it is now a thin wrapper over the new private
  annotateAndRepublish(ctx, annotation, withDemo bool) in
  src/services/plan_orchestrator.go. withDemo=true (the wrapper, used by
  drainAnnotations/ServeAnnotations) keeps the plan-section demo refresh;
  confirmAssumptions calls annotateAndRepublish(..., false) directly, so an
  assumptions correction annotates + republishes but never runs DemoInvocation.
- The single demo generation for a corrected plan is the end-of-run
  refreshDemo in Run (fires when planSucceeded). Goal-section annotations are
  unaffected: rebuildFromGoal still refreshes the demo itself.
- Any future pre-refinement annotation path should also pass withDemo=false —
  a demo generated before refine is discarded and regenerated.
- Tests: TestAssumptionsCorrectionDefersDemoUntilAfterRefinement
  (plan_orchestrator_test.go) asserts the full invocation order
  plan,annotate,assess,demo — demo strictly after the assess pass — with
  DemoInvocation+DemoFile set and a reporter attached, and that the end-of-run
  demo is published to the reporter (reportFinish → reportPlan reads DemoFile).
  TestPostCompletionPlanAnnotationStillRefreshesDemo (plan_annotation_test.go)
  asserts ServeAnnotations still runs annotate,demo for a plan annotation.
- invocationPrompts helper (plan_annotation_test.go) reads inv.Args[1]; demo
  invocations in tests must keep the {"-p", "demo"} arg shape.
- make test exit 0.

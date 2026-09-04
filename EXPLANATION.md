Determined now scales planning ceremony to the goal. PLAN.md records an auditable trivial/small/medium/large classification, Go enforces its step and test caps, and one plan-time simplicity pass can remove or merge speculative work before refinement. Interview answers clarify but never widen GOAL.md, while accepted trade-offs remain advisory during specialist review.

Execution verification now costs one correctness reviewer per checked step by default. The former per-step simplicity review remains available with `--step-simplicity`; this keeps the guard available without charging every ordinary execution for work already performed at plan time.

This run also retains determined's earlier planning gates and status-server hardening: assumptions remain confirmable, exhausted refinement and misaligned tests still require explicit choices, and state-changing status endpoints remain token-protected.

## Assumption confirmation round after drafting

The plan prompt now requires every assumption and chosen default to live under a `## Assumptions` heading, and after the draft the orchestrator relays that list as one question. Bare Enter, `y`, `yes`, or `ok` confirms; any other answer is treated as a correction and applied through the annotation path, then the documents are republished. This closes the "use sensible defaults" gap where the AI's guesses became the plan without the user ever seeing them. An old-format plan without the section skips the round entirely.

```diff
--- a/src/services/plan_orchestrator.go
+++ b/src/services/plan_orchestrator.go
@@ -161,6 +162,9 @@ func (o *PlanOrchestrator) create(ctx context.Context, deadline time.Time) model
 			if outcome, stop := o.ensureTests(ctx); stop {
 				return outcome
 			}
+			if outcome, stop := o.confirmAssumptions(ctx); stop {
+				return outcome
+			}
 			return o.refine(ctx, deadline)
@@ -323,8 +330,57 @@ func (o *PlanOrchestrator) useExistingGoal() (bool, error) {
+func (o *PlanOrchestrator) confirmAssumptions(ctx context.Context) (models.Outcome, bool) {
+	assumptions := o.docs.Assumptions()
+	if assumptions == "" {
+		return models.OutcomePlanReady, false
+	}
+	writeProgress(o.terminal, o.clock, "confirming plan assumptions")
+	if o.status != nil {
+		o.status.WaitForInput()
+	}
+	answer, err := o.prompter.Ask(assumptionsQuestion(assumptions))
+	if err != nil {
+		fmt.Fprintf(o.terminal, "determined: could not read your answer: %v\n", err)
+		return models.OutcomeInterrupted, true
+	}
+	if assumptionsConfirmed(answer) {
+		return models.OutcomePlanReady, false
+	}
+	o.annotateAndRepublish(ctx, models.Annotation{
+		Section: models.AnnotationSectionPlan,
+		Target:  "Assumptions",
+		Comment: answer,
+	}, false)
+	return models.OutcomePlanReady, false
+}
```

The section is extracted by a tolerant parser on the document publisher, so the orchestrator has one access path and legacy plans never error:

```diff
--- a/src/services/plan_document_publisher.go
+++ b/src/services/plan_document_publisher.go
@@ -47,6 +51,48 @@ func (p *PlanDocumentPublisher) PublishPlan(sink PlanDocumentSink) {
+// Assumptions returns the `## Assumptions` section body from PLAN.md, or ""
+// when the plan or the heading is absent. Old-format plans without the
+// section are valid, so this never reports an error.
+func (p *PlanDocumentPublisher) Assumptions() string {
+	plan, err := p.files.Read(p.cfg.PlanFile)
+	if err != nil {
+		return ""
+	}
+	return assumptionsSection(plan)
+}
```

## Explicit approval before chained execution

A finished plan no longer flows straight into execution. Both the headless `-plan ... -exec` chain and the interactive auto-exec path now pass through one shared gate that defaults to No: only `y`/`yes` starts the execute loop; bare Enter, any other text, or a failed read leaves the plan files intact with the plan-ready outcome. The status page shows the wait so a browser user knows to return to the terminal.

```diff
--- a/cmd/determined/plan_flow.go
+++ b/cmd/determined/plan_flow.go
+// approveExecution gates chained execution on explicit user consent: only a
+// y/yes answer starts the execute loop. Anything else — bare Enter, any other
+// text, or a failed read — declines, leaving the plan files intact.
+func approveExecution(prompter services.Prompter, status inputWaiter) bool {
+	if status != nil {
+		status.WaitForInput()
+	}
+	answer, err := prompter.Ask("Plan approved — begin execution? [y/N]")
+	if err != nil {
+		return false
+	}
+	normalized := strings.ToLower(strings.TrimSpace(answer))
+	return normalized == "y" || normalized == "yes"
+}
```

```diff
--- a/cmd/determined/main.go
+++ b/cmd/determined/main.go
@@ -160,14 +162,15 @@ func main() {
-			outcome = runPlan(ctx, selected, planInput(*plan, flag.Args()), planMode, *budget, *maxStepPasses, *maxFailures, *interactive, executing, executor, clock, logs)
-			if shouldExecuteAfterPlan(executing, *interactive, outcome) {
-				outcome = runHeadlessExec(ctx, executionTool, executor, clock)
+			outcome = runPlan(ctx, selected, planInput(*plan, flag.Args()), planMode, *budget, *maxStepPasses, *maxFailures, *interactive, executing, *statusHost, executor, clock, logs)
+			if shouldExecuteAfterPlan(executing, *interactive, outcome) &&
+				approveExecution(clients.NewStdinPrompter(os.Stdout, os.Stdin), nil) {
+				outcome = runHeadlessExec(ctx, executionTool, *statusHost, executor, clock)
 			}
@@ -569,6 +573,9 @@ func runInteractivePlan(...)
 	case postPlanAutoExec:
+		if !approveExecution(clients.NewStdinPrompter(os.Stdout, os.Stdin), status) {
+			return outcome
+		}
 		fmt.Fprintf(os.Stdout, "determined: plan ready — executing now; status page streaming at %s\n", server.URL())
```

## Refine-cap exhaustion gates on accept, refine, or edit

The old refine loop hit its pass cap, printed the remaining finding count, deleted REFINEMENTS.md, and returned `OutcomePlanReady` — silently shipping a plan with known defects. Now the unresolved findings are printed to the terminal and published to the status page, and the loop blocks on an explicit choice: `accept` finishes as-is, `refine` grants exactly one more pass (a still-dirty pass re-asks — there is no auto-accept path), and `edit` waits for the user to fix PLAN.md/STEPS.md by hand, then re-assesses. Stop (a failed read) stalls the plan.

```diff
--- a/src/services/plan_orchestrator.go
+++ b/src/services/plan_orchestrator.go
@@ -358,17 +414,21 @@ func (o *PlanOrchestrator) refine(ctx context.Context, deadline time.Time) model
 		if pass >= o.cfg.MaxRefinePasses {
-			fmt.Fprintf(o.terminal,
-				"determined: %d planning issue(s) remain after %d refine pass(es); leaving the plan as-is\n",
-				len(issues), pass)
-			o.files.Remove(o.cfg.AssessmentFile)
-			return models.OutcomePlanReady
+			outcome, action := o.gateExhaustedCap(issues, pass)
+			if action == capReturn {
+				return outcome
+			}
+			if action == capReassess {
+				continue
+			}
+			// capRefineMore: fall through to one more refine pass; a
+			// still-dirty assessment brings the loop back to this gate.
 		}
```

```diff
--- a/src/services/plan_orchestrator.go
+++ b/src/services/plan_orchestrator.go
+func (o *PlanOrchestrator) gateExhaustedCap(issues []string, pass int) (models.Outcome, refineCapAction) {
+	o.publishRemainingFindings(issues, pass)
+	switch o.askCapChoice() {
+	case capChoiceAccept:
+		o.files.Remove(o.cfg.AssessmentFile)
+		return models.OutcomePlanReady, capReturn
+	case capChoiceEdit:
+		return o.awaitPlanEdits()
+	case capChoiceStop:
+		return models.OutcomePlanStalled, capReturn
+	}
+	return models.OutcomePlanReady, capRefineMore // outcome ignored
+}
```

## Misaligned tests get an automatic realign pass, then a user gate

`ensureAlignment` previously stopped at verdict presence: a test judged `misaligned` still counted as done. Now a non-empty misaligned set triggers one automatic `RealignInvocation` — a new prompt that rewrites only misaligned sections in place, keeping everything else verbatim — and any test still misaligned afterwards blocks on accept / rewrite / drop. `rewrite` re-runs the realign invocation; `drop` runs the same invocation with a deletion constraint appended to its prompt; both re-parse the verdicts and re-ask while misaligned verdicts remain, so no path reaches `OutcomePlanReady` with a standing misaligned verdict the user did not accept.

```diff
--- a/src/services/plan_orchestrator.go
+++ b/src/services/plan_orchestrator.go
@@ -609,7 +769,7 @@ func (o *PlanOrchestrator) ensureAlignment(ctx context.Context) (models.Outcome,
 	if len(missing) == 0 {
-		return models.OutcomePlanReady, false
+		return o.realignMisaligned(ctx)
 	}
@@ -624,7 +784,157 @@
-	return models.OutcomePlanReady, false
+	return o.realignMisaligned(ctx)
+}
+
+func (o *PlanOrchestrator) realignMisaligned(ctx context.Context) (models.Outcome, bool) {
+	misaligned, err := o.misalignedTests()
+	if err != nil {
+		fmt.Fprintf(o.terminal, "determined: could not read %s: %v\n", o.cfg.TestsFile, err)
+		return models.OutcomePlanStalled, true
+	}
+	if len(misaligned) == 0 {
+		return models.OutcomePlanReady, false
+	}
+	if outcome, stop := o.runInvocation(ctx, o.cfg.RealignInvocation, "realigning tests"); stop {
+		return outcome, stop
+	}
+	return o.gateMisaligned(ctx)
+}
```

The verdict data comes from a new parser on `TestsDocument` that pairs each misaligned heading with the assessor's explanation:

```diff
--- a/src/services/tests_document.go
+++ b/src/services/tests_document.go
+// MisalignedTests returns every test whose `**Alignment:**` verdict is
+// misaligned, with its `**Alignment note:**` text. A missing note yields an
+// empty Note; verdict-free, aligned, and partial sections are skipped.
+func (d TestsDocument) MisalignedTests() []MisalignedTest {
+	misaligned := []MisalignedTest{}
+	for _, section := range d.sections() {
+		match := alignmentLinePattern.FindStringSubmatch(section)
+		if match == nil || !strings.EqualFold(match[1], "misaligned") {
+			continue
+		}
+		misaligned = append(misaligned, MisalignedTest{
+			Heading: strings.SplitN(strings.TrimSpace(section), "\n", 2)[0],
+			Note:    d.alignmentNote(section),
+		})
+	}
+	return misaligned
+}
```

## Status server locked to loopback with token-guarded endpoints

The status server used to bind `0.0.0.0` with no authentication, so any host on the network could read logs and POST `/implement` to start unattended execution — defeating the approval gate above. Three layers close this. First, the bind defaults to `127.0.0.1`; a new `-status-host` flag makes wider exposure an explicit opt-in. Second, a `Host` allowlist on every request defeats DNS rebinding, and matching `Origin` validation guards the WebSocket upgrade. Third, an unguessable 32-byte per-session token is injected into the served page and required on every state-changing endpoint (`/implement`, `/annotate`, `/task/skip`, `/task/stop`, `/stall/choice`, `/explain/start`, `/chat/ask`), so a cross-site page — which cannot read the served document — cannot forge those POSTs even from the user's own browser.

```diff
--- a/src/clients/plan_status_server.go
+++ b/src/clients/plan_status_server.go
 func (s *PlanStatusServer) Start() error {
-	listener, err := net.Listen("tcp", "0.0.0.0:0")
+	token, err := newSessionToken()
+	if err != nil {
+		return err
+	}
+	s.token = token
+	listener, err := net.Listen("tcp", net.JoinHostPort(s.host, "0"))
 	if err != nil {
 		return fmt.Errorf("could not bind status server: %w", err)
 	}
 	s.listener = listener
 	s.server = &http.Server{
-		Handler:           s.routes(),
+		Handler:           s.hostGuard(s.routes()),
```

```diff
--- a/src/clients/plan_status_server.go
+++ b/src/clients/plan_status_server.go
+// authorized admits requests presenting the per-session token; everything else
+// — including requests arriving before Start assigned one — is refused.
+func (s *PlanStatusServer) authorized(w http.ResponseWriter, r *http.Request) bool {
+	presented := r.Header.Get(StatusSessionTokenHeader)
+	if s.token != "" && subtle.ConstantTimeCompare([]byte(presented), []byte(s.token)) == 1 {
+		return true
+	}
+	http.Error(w, "missing or invalid session token", http.StatusForbidden)
+	return false
+}
```

The page reads the token from a meta tag and stamps it on every state-changing fetch:

```diff
--- a/src/clients/plan_status_page.html
+++ b/src/clients/plan_status_page.html
+<meta name="session-token" content="{{SESSION_TOKEN}}">
...
+  const sessionToken = document.querySelector('meta[name="session-token"]').content;
+  const sessionHeaders = { "X-Session-Token": sessionToken };
...
-    fetch("/implement", { method: "POST" }).then(() => {
+    fetch("/implement", { method: "POST", headers: sessionHeaders }).then(() => {
```

## Status page cannot be framed

The token defense leaves one browser hole: a hostile site could iframe the loopback page — which carries the legitimate token — and clickjack the one-click Implement button. Every page response now forbids embedding.

```diff
--- a/src/clients/plan_status_server.go
+++ b/src/clients/plan_status_server.go
@@ -224,7 +339,12 @@ func (s *PlanStatusServer) servePage(w http.ResponseWriter, r *http.Request) {
 	w.Header().Set(StatusPageHeader, "1")
-	w.Write(planStatusPage) //nolint:errcheck // best-effort page write
+	// The status page is never legitimately framed; forbid embedding so a
+	// hostile site cannot clickjack the token-bearing Implement button.
+	w.Header().Set("X-Frame-Options", "DENY")
+	w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
+	page := bytes.ReplaceAll(planStatusPage, []byte(statusSessionTokenPlaceholder), []byte(s.token))
+	w.Write(page) //nolint:errcheck // best-effort page write
```

## Assessor questions reach the user in create mode

The assessment prompt now separates objective findings (REFINEMENTS.md) from preference-dependent choices, which become concrete questions in QUESTIONS.md — and the refine loop relays those questions in both modes instead of only during plan review, recording answers in ANSWERS.md before the refiner runs. The refinement prompt treats those answers as authoritative, so user intent shapes the rewrite rather than the assessor guessing.

```diff
--- a/src/services/plan_orchestrator.go
+++ b/src/services/plan_orchestrator.go
@@ -358,17 +414,21 @@ func (o *PlanOrchestrator) refine(...)
-		if o.cfg.Operation == models.PlanOperationReview && o.files.Exists(o.cfg.QuestionsFile) {
+		if o.files.Exists(o.cfg.QuestionsFile) {
 			if outcome, stop := o.relayQuestions(); stop {
 				return outcome
 			}
 		}
```

```diff
--- a/src/services/planning_prompts.go
+++ b/src/services/planning_prompts.go
-		"Write each specific, actionable finding as a markdown list item in REFINEMENTS.md. " +
-		"If there are no findings, write exactly NONE. Do not modify the plan or implement anything."
+		"Write each specific, actionable objective finding as a markdown list item in REFINEMENTS.md. " +
+		"If there are no findings, write exactly NONE. " +
+		"For each consequential finding that depends on user preference, product intent, or risk tolerance, also write one concrete " +
+		"question to QUESTIONS.md as a markdown numbered list; include options and tradeoffs when useful. " +
+		"Do not ask about choices that can be safely inferred, and do not resolve preference-dependent findings yourself. " +
+		"Do not modify the plan or implement anything."
```

## Assumption corrections skip the wasted demo regeneration

`applyAnnotation` regenerates the UI demo for any plan-section annotation, but the assumptions round runs before refinement — a demo built there would come from a plan the refine loop is about to rewrite, and `Run` regenerates the demo again at the end anyway. The correction path now uses a variant with a caller-controlled demo step, saving one full AI invocation per corrected assumption, while post-completion page annotations keep their refresh.

```diff
--- a/src/services/plan_orchestrator.go
+++ b/src/services/plan_orchestrator.go
 func (o *PlanOrchestrator) applyAnnotation(ctx context.Context, annotation models.Annotation) {
+	o.annotateAndRepublish(ctx, annotation, true)
+}
+
+// annotateAndRepublish is applyAnnotation with a caller-controlled demo step.
+// The assumptions-correction round passes withDemo=false: it runs before the
+// refine loop rewrites the plan, and Run regenerates the demo once after
+// planning succeeds, so a demo generated here would be discarded unused.
+func (o *PlanOrchestrator) annotateAndRepublish(ctx context.Context, annotation models.Annotation, withDemo bool) {
@@ -725,7 +1045,7 @@
-	if annotation.Section == models.AnnotationSectionPlan {
+	if annotation.Section == models.AnnotationSectionPlan && withDemo {
 		o.refreshDemo(ctx)
 	}
```

## One automated journey exercises every gate together

`cmd/determined/plan_journey_test.go` adds a single test that drives the whole TESTS.md Test 1 sequence against the fake runner, FileStore, and Prompter: an assumptions correction that is annotated and republished, a create-mode assessor question recorded in ANSWERS.md before the refiner runs, a `misaligned` verdict fixed by the automatic realign pass, the refine cap exhausting with findings answered `accept`, and the chained-exec approval declined with bare Enter — asserting the execution loop is never invoked and the plan files survive. The per-gate unit tests prove each gate in isolation; this test is the only one that would catch a regression in their ordering or the state carried between them. A companion regression suite (`src/services/plan_gates_regression_test.go`) proves old-format artifacts — plans without `## Assumptions`, verdict-free TESTS.md, absent QUESTIONS.md — and nil status reporters keep working through the new gates.

# FIXES

## Security review finding: unauthenticated network path bypasses the exec approval gate

**Date:** 2026-08-02
**Reviewer:** independent security review of the alignment-gates work (steps 1–14)

**Issue.** Step 12's invariant — "Unattended execution, which edits files freely,
never starts from a plan the user has not signed off on" — is enforced only on
the terminal paths. The status server binds all interfaces with no
authentication, and its `/implement` endpoint starts the execution loop
directly, so any host that can reach the port can begin unattended execution
of a plan the user never approved. The executing tools run with elevated
autonomy (`droid --auto high`, `claude --permission-mode acceptEdits`), so the
consequence is unattended file editing triggered remotely.

**Evidence.**
- `src/clients/plan_status_server.go:151` — `net.Listen("tcp", "0.0.0.0:0")`;
  the doc comment at `plan_status_server.go:185` explicitly invites remote
  browsers ("remote users substitute the machine's external IP").
- `src/clients/plan_status_server.go:360` (`serveImplement`) — accepts any
  `POST /implement` with no credential, origin check, or confirmation and
  calls `RequestImplement()`.
- `cmd/determined/main.go:748` — `FeedbackActionImplement` invokes
  `execute(ctx, status)` with no `approveExecution` call; the new gate added
  in `cmd/determined/plan_flow.go` (`approveExecution`) is called only from
  the headless `-exec` chain and `postPlanAutoExec`.
- PLAN.md's assumption "the page's existing Implement button remains its own
  affirmative act and needs no second prompt" presumes only the user can
  click it; with an unauthenticated non-loopback listener that presumption
  does not hold. Repro from any LAN peer:
  `curl -X POST http://<host>:<port>/implement` → HTTP 202 and execution
  begins once planning has completed.

**Recommended remediation (either closes the gap).**
1. Bind the status server to `127.0.0.1` by default, with remote exposure an
   explicit opt-in; and/or
2. Require an unguessable per-session token on state-changing endpoints
   (`/implement` at minimum; `/annotate`, `/task/stop`, `/task/skip`,
   `/stall/choice`, `/chat/ask` share the same exposure), embedded in the
   served page so the legitimate browser session keeps working.

No fix was implemented during this review.

**Resolution (step 15, commit a2d61f8).** The status server now binds
`127.0.0.1` by default; non-loopback exposure requires the explicit
`-status-host` opt-in. `tests/plan_status_server_bind_test.go` proves a LAN
peer cannot connect under the default bind. This closes the network-peer
vector described above.

## Security review finding: loopback bind does not stop the user's own browser (CSRF and DNS rebinding)

**Date:** 2026-08-02
**Reviewer:** independent security review of the step-15 remediation

**Issue.** The step-15 fix (loopback bind) blocks LAN peers but not requests
issued *by the user's own browser* on behalf of a hostile web page. The server
performs no `Host` header validation, no `Origin`/`Referer` check on
state-changing POSTs, no CSRF token, and no per-session credential; the
WebSocket upgrade (`/chat`) also ignores `Origin`. Two browser-borne vectors
remain while a session is running:

1. **Cross-site request forgery.** `POST /implement` requires no body, no
   custom header, and no non-simple content type, so it is a CORS "simple
   request": a hostile page the user visits can issue
   `fetch("http://127.0.0.1:<port>/implement", {method: "POST", mode: "no-cors"})`
   and the side effect fires even though the response is opaque. The same
   applies to `/task/stop`, `/task/skip`, `/explain/start`, and — with a
   `text/plain` body that still decodes as JSON — `/annotate` and
   `/stall/choice` (annotations feed directly into subsequent AI prompts, so
   this is also a prompt-injection channel). The ephemeral port is the only
   obstacle, and localhost ports are enumerable from JavaScript.
2. **DNS rebinding.** An attacker-controlled domain that rebinds to
   `127.0.0.1` makes the status server same-origin with the hostile page.
   Because no handler checks `Host`, the page can then *read* responses —
   session status, full log history via `/logs` (tool output may include file
   contents), and the `/chat` WebSocket — and drive every endpoint, fully
   reproducing the original finding's impact from a remote web page.

**Evidence.**
- `src/clients/plan_status_server.go` — no handler inspects `r.Host`,
  `Origin`, or `Referer`; `serveImplement` (line 373) accepts any
  loopback-delivered POST.
- `src/clients/websocket.go:76` (`UpgradeWebSocket`) — validates the upgrade
  headers only; no `Origin` check.
- `grep -rn "Origin" src/clients/` returns no origin validation.
- Consequence unchanged from the prior finding: `RequestImplement()` starts
  unattended execution with elevated-autonomy tools
  (`droid --auto high`, `claude --permission-mode acceptEdits`).

**Recommended remediation (both are needed; each kills one vector).**
1. Reject any request whose `Host` is not `localhost`/`127.0.0.1`/`[::1]`
   (with the bound port) unless the caller opted into a non-loopback bind —
   this defeats DNS rebinding. Apply the same allowlist to the WebSocket
   upgrade's `Origin` header when one is present.
2. Require an unguessable per-session token on every state-changing endpoint
   (`/implement`, `/annotate`, `/task/skip`, `/task/stop`, `/stall/choice`,
   `/explain/start`, `/chat/ask`), generated at server start and embedded in
   the served page (e.g. injected into the HTML and sent as a request
   header) — this defeats cross-site POSTs regardless of bind host and also
   covers the documented `-status-host 0.0.0.0` opt-in.

No fix was implemented during this review.

**Resolution (browser-borne defense step).** Both remediations are in place in
`src/clients/plan_status_server.go`: a `Host` allowlist
(`localhost`/`127.0.0.1`/`[::1]`, bound port) enforced on every request while
the server is bound to loopback (`hostGuard`), matching `Origin` validation on
the `/chat` WebSocket upgrade (absent `Origin` still admitted for the CLI
dialer), and a per-session token generated at `Start`, injected into the served
page's `session-token` meta tag, and required via the `X-Session-Token` header
on `/implement`, `/annotate`, `/task/skip`, `/task/stop`, `/stall/choice`,
`/explain/start`, and `/chat/ask` regardless of bind host.
`tests/plan_status_server_browser_guard_test.go` proves the foreign-Host
reject, the tokenless-POST reject with the page-token flow still working, and
the foreign-Origin upgrade reject.

## Security review finding: status page is frameable — clickjacking defeats the token defense

**Date:** 2026-08-02
**Reviewer:** independent security review of the step-15/16 remediations

**Issue.** The step-16 session token stops a hostile page from *forging* a
state-changing request, but not from *borrowing the user's own page*: the served
status page carries no anti-framing defense (`X-Frame-Options` or
`Content-Security-Policy: frame-ancestors`), so a hostile site can embed
`http://127.0.0.1:<port>/` in an iframe (loopback origins are "potentially
trustworthy", so mixed-content blocking does not stop an HTTPS attacker page
framing them; localhost ports are enumerable from JavaScript via fetch timing).
Inside the frame the legitimate page runs with its own embedded token and
attaches it to every POST. The Implement button fires its request on a single
click with no confirmation dialog, so an overlay/opacity clickjack that steers
one user click onto the framed button starts unattended execution — with the
session token, through the exact flow steps 15–16 were built to protect. Skip,
Stop, and stall-choice controls are exposed the same way (their
`window.confirm` adds friction, not protection, since the dialog can itself be
disguised as belonging to the attacker page).

**Evidence.**
- `src/clients/plan_status_server.go:333` (`servePage`) — sets only
  `Content-Type` and the status-page marker header; no `X-Frame-Options`, no
  `Content-Security-Policy`. `grep -rn "X-Frame-Options\|frame-ancestors"
  src/ cmd/` finds no page-level header anywhere.
- `src/clients/plan_status_page.html:1323` — the Implement click handler posts
  `/implement` immediately with `sessionHeaders`; no confirmation step.
- The token defense is bypassed by design here: the request originates from the
  genuine page, so `authorized` (`plan_status_server.go:272`) passes.

**Recommended remediation.** Set `X-Frame-Options: DENY` and
`Content-Security-Policy: frame-ancestors 'none'` on every served page response
(`servePage`); the page is opened directly in a tab and never legitimately
framed, so `DENY`/`'none'` costs nothing.

No fix was implemented during this review.

## Audit finding: TESTS.md Test 1 journey has no single automated test

**Date:** 2026-08-02
**Reviewer:** post-completion audit of STEPS.md against PLAN.md and TESTS.md

**Issue.** TESTS.md Test 1 ("Attended planning journey with every alignment
gate", type end-to-end journey, verdict aligned) is required to exist as an
automated test. It does not: its behavior is covered only piecewise —
`TestPlanAssumptionsCorrectionAnnotatesAndRepublishes`,
`TestPlanRefineRelaysAssessorQuestionsInCreateMode`,
`TestPlanRealignsMisalignedTestsOnceWithoutAsking`,
`TestExhaustedCapAcceptCompletesAndPublishesFindings`, and
`TestDeclinedChainedExecutionKeepsPlanFilesAndPlanReadyOutcome` — plus the
step-14 manual run recorded in NOTES.md. The closest single-run test,
`TestNewGatesRunWithNilStatusReporter` (src/services/plan_gates_regression_test.go:117),
differs materially from the journey: it confirms assumptions with bare Enter
instead of applying a correction, resolves the misaligned verdict via the
gate's `accept` instead of the automatic realign fixing it to `aligned`, and
stops at the orchestrator boundary — the exec-approval decline tail (bare
Enter, files intact, execution loop never invoked) is never part of the same
run. No automated test proves the gates compose in one pass.

**Disposition.** All 16 STEPS.md steps individually satisfy their Done-when
criteria (verified against the test suite; `make test` green), so no step was
unchecked. A new step was appended to STEPS.md requiring the Test 1 journey
to be implemented as one automated test and passing. TESTS.md Tests 2 and 3
(BDD) are satisfied by the create-mode relay and misaligned-gate test groups
respectively.

## Performance review finding: assumption correction runs a wasted demo AI invocation before refinement

**Date:** 2026-08-02
**Reviewer:** independent performance review of the alignment-gates work

**Issue.** The step-4 assumption confirmation round reuses `applyAnnotation`
for the correction path, and `applyAnnotation` regenerates the UI demo for any
plan-section annotation. That behavior was built for post-completion page
annotations, where the refreshed demo is the deliverable. Invoked from
`confirmAssumptions` — which runs after drafting and *before* `refine` — it
launches a full demo tool invocation (droid/claude, minutes of wall clock and
token spend) against a plan that the refine loop is about to rewrite, and
`Run` then deletes and regenerates that same demo after planning succeeds. The
mid-planning demo artifact is discarded by construction: every interactive
create-mode session in which the user corrects an assumption pays one entire
extra AI invocation and blocks the attended flow on it before refinement can
start.

**Evidence.**
- `src/services/plan_orchestrator.go:356` — `confirmAssumptions` applies a
  correction as `applyAnnotation(..., Section: AnnotationSectionPlan, ...)`.
- `src/services/plan_orchestrator.go:1037` — `applyAnnotation` calls
  `o.refreshDemo(ctx)` for every plan-section annotation.
- `src/services/plan_orchestrator.go:140` — `refreshDemo` removes the demo
  file and, when a status reporter is attached, runs `DemoInvocation` — a
  full AI tool run.
- `src/services/plan_orchestrator.go:156` (`create`) — `confirmAssumptions`
  runs before `refine`, so refine passes rewrite the plan the demo was just
  generated from.
- `src/services/plan_orchestrator.go:127` — `Run` calls `refreshDemo` again
  once the outcome is plan-ready, deleting and regenerating the demo.
- `cmd/determined/main.go:407` and `:411` — the interactive create config
  carries both `DemoInvocation` and `DemoFile: "DEMO.html"`, so the path is
  live in production; the unit tests never see it because `planConfig(0)` in
  `src/services/plan_orchestrator_test.go` leaves both empty.

**Recommended remediation.** The correction path should stage the annotation,
run the annotate invocation, and republish — without the demo refresh
(`applyAnnotation` gains a caller-controlled demo step, or `confirmAssumptions`
uses a variant that skips `refreshDemo`). The end-of-run `refreshDemo` in
`Run` already produces the demo from the final refined plan, so no user-visible
capability is lost.

No fix was implemented during this review.

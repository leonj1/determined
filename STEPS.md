# STEPS

Ordered, individually-completable steps to make `determined` more accurate over
long unattended runs. Each step is one focused change. Mark a step `[x]` only
when its "Done when" condition is verified.

- [x] 1. Enforce a machine-checkable step format at plan time. Update `planPrompt` and `breakdownPrompt` in `cmd/determined/main.go` so STEPS.md must be written as a markdown checkbox list (`- [ ]` per step) and every step must end with a `Done when:` line stating a concrete, checkable acceptance condition (a command to run or behavior to observe).
  Done when: both prompts state the checkbox and `Done when:` requirements, and `go test ./...` passes.

- [x] 2. Add a STEPS.md parser to `src/services`. Implement a function that parses checkbox-format STEPS.md content into a slice of steps, each with its text, its `Done when:` criterion (if present), and completed status, plus helpers for "next incomplete step" and "all complete". Include unit tests covering completed, incomplete, malformed, and empty inputs.
  Done when: the parser and its tests exist, and `go test ./src/services/...` passes with the new tests.

- [x] 3. Inject the specific next step into the execute prompt. Change `Orchestrator` to read STEPS.md via the parser each iteration and build the iteration prompt as "Work on exactly this step and no other: <step text>. Its acceptance criterion: <Done when>. Mark it `[x]` in STEPS.md when done." This replaces the hardcoded verbatim prompt (including its "has needs to be completed" typo). `Config` gains what it needs (a FileStore and the steps filename); update `cmd/determined/main.go` wiring and the orchestrator fakes/tests.
  Done when: each iteration's invocation embeds the parsed next step, the old hardcoded execute prompt is gone, and `go test ./...` passes.

- [x] 4. Stop trusting STOP.md. The orchestrator decides completion itself: when the parser reports all steps complete, the run ends with `OutcomeStopped` (write STOP.md for compatibility). If STOP.md exists while unchecked steps remain, delete it, log a warning to the terminal, and continue looping. Update tests.
  Done when: a test proves a premature STOP.md is deleted and the loop continues, a test proves the loop ends when all boxes are checked, and `go test ./...` passes.

- [x] 5. Fail fast on missing or stale protocol files. In execute mode, exit immediately with a clear error if PLAN.md or STEPS.md is missing at startup. At startup, if a stale STOP.md exists alongside unchecked steps, delete it with a warning instead of exiting instantly as success.
  Done when: running execute mode in a directory without PLAN.md/STEPS.md exits non-zero with a message naming the missing file, covered by a test, and `go test ./...` passes.

- [x] 6. Add stall detection. Before each iteration, snapshot the parsed completed-step count; if a configurable number of consecutive iterations (default 3, flag `--max-stalled-iterations`) complete without a newly checked step, end the run with a new `OutcomeStalled` outcome and distinct exit code. Update EXECUTION.md's exit-code table.
  Done when: a test drives N no-progress iterations and observes `OutcomeStalled`, and `go test ./...` passes.

- [x] 7. Retry failed invocations instead of aborting. On a non-zero tool exit, retry the same iteration up to a cap of consecutive failures (default 3, flag `--max-consecutive-failures`), resetting the counter after any success. Only after the cap is hit does the run end with `OutcomeDroidFailed`. Interruptions (`ctx.Err()`) still stop immediately.
  Done when: tests cover retry-then-succeed and cap-exhausted-then-abort, and `go test ./...` passes.

- [x] 8. Add a per-invocation timeout. New flag `--max-iteration-duration` (default 15m, 0 = unlimited) wraps each `runner.Run` in `context.WithTimeout` so a hung tool invocation cannot hang the loop forever. A timed-out invocation counts as a failed invocation for step 7's retry logic, not an interruption.
  Done when: a test with a fake runner that never returns observes the timeout being enforced, and `go test ./...` passes.

- [x] 9. Add an independent verification pass after each step. After an iteration marks a step complete, run a fresh tool invocation with a reviewer prompt: "Step N claims complete: <step text>. Acceptance criterion: <Done when>. Verify by reading the code and running the stated check. If not genuinely done, uncheck it in STEPS.md and append what is wrong to FIXES.md; if done, do nothing." The loop re-runs a step the verifier unchecks. Add flag `--verify` (default on) to disable it. Guard against ping-pong by counting a verifier rejection toward step 6's stall counter.
  Done when: tests cover verifier-passes (loop advances) and verifier-unchecks (same step re-runs with FIXES.md present), and `go test ./...` passes.

- [x] 10. Carry knowledge between iterations via NOTES.md. Extend the injected execute prompt: "Read NOTES.md if it exists before starting. Before finishing, append to NOTES.md any decisions, conventions, or gotchas later steps need to know." Mention NOTES.md in EXECUTION.md.
  Done when: the injected prompt includes the NOTES.md instructions, covered by a prompt-construction test, and `go test ./...` passes.

- [x] 11. Git-checkpoint each verified step. After the verifier (step 9) approves a step, the orchestrator runs `git add -A && git commit -m "determined: step N: <step text>"` when the working directory is a git repository; skip silently (with a terminal note) when it is not. Add flag `--git-checkpoint` (default on) to disable.
  Done when: a test with a fake runner asserts the commit invocation is issued after a verified step and skipped when disabled, and `go test ./...` passes.

- [x] 12. Make the `claude` tool viable unattended. Change `ClaudeTool.Invocation` in `src/models/tool.go` to run `claude -p "<prompt>" --permission-mode acceptEdits` so print-mode runs do not stall on permission prompts, and document the flag choice and its safety trade-off in README.md.
  Done when: `tool_test.go` asserts the new arguments and `go test ./...` passes.

- [ ] 13. Reconcile the README `--auto` documentation with the code. README.md documents an `--auto` flag (default `medium`) that `cmd/determined/main.go` does not define; `src/models/tool.go` hardcodes droid autonomy to `high`. Either add the flag and thread it through `DroidTool`, or remove the flag row and the "Required for unattended runs" note from README.md — pick one and make code and docs agree.
  Done when: README.md and the actual flag set of the binary agree about `--auto`, and `go test ./...` passes.

- [ ] 14. Add a final whole-plan audit before declaring success. When all steps are checked, run one fresh invocation: "Read PLAN.md and STEPS.md. Audit whether the implementation genuinely satisfies the plan. If a step is not actually satisfied, uncheck it and append the reason to FIXES.md; if everything is satisfied, create STOP.md." Only when STOP.md exists *and* all steps are checked does the run end successfully; an audit that unchecks steps sends the loop back to step execution.
  Done when: tests cover audit-approves (run ends `OutcomeStopped`) and audit-reopens (loop resumes on the unchecked step), and `go test ./...` passes.

- [ ] 15. Update the documentation for the new loop behavior. Revise README.md, EXECUTION.md, and PLANNING.md to describe: orchestrator-parsed checkboxes, per-step prompt injection, the verification and audit passes, stall detection, retries, per-invocation timeout, NOTES.md/FIXES.md, git checkpointing, and all new flags with defaults. Remove the now-closed "Known limitation" section.
  Done when: every new flag and protocol file appears in the docs, the stale "Known limitation" section is gone, and `go test ./...` passes.

## ADDED Requirements

### Requirement: Interactive flag gating
The CLI SHALL accept a `-interactive` boolean flag. The flag SHALL be valid only in combination with `-plan`. When `-interactive` is supplied without `-plan`, the program SHALL exit with a usage error and SHALL NOT start planning or a web server.

#### Scenario: Interactive with plan
- **WHEN** the user runs `determined -plan "goal" -interactive`
- **THEN** a local web server starts before the planning session begins and its URL is printed to the terminal

#### Scenario: Interactive without plan
- **WHEN** the user runs `determined -interactive` (no `-plan`)
- **THEN** the program exits with a non-zero status and a usage error naming the invalid flag combination, and no server is started

#### Scenario: Plan without interactive
- **WHEN** the user runs `determined -plan "goal"` without `-interactive`
- **THEN** planning behaves exactly as before with no web server started

### Requirement: Local status web server
When `-plan -interactive` is active, the system SHALL start an HTTP server bound to the loopback interface serving a single HTML status page and a server-sent events endpoint. If the server fails to start, the program SHALL report the error and exit without running the planning session.

#### Scenario: Server startup
- **WHEN** `-plan -interactive` starts successfully
- **THEN** the terminal shows a URL of the form `http://127.0.0.1:<port>/` and fetching it returns the status page

#### Scenario: Server bind failure
- **WHEN** the loopback listener cannot be created
- **THEN** the program prints the bind error and exits with a failure outcome without invoking the AI tool

### Requirement: Git context header
The status page SHALL display the working directory's git remote URL and current branch in the top header. When the directory has no remote or no branch (e.g., not a repository or detached HEAD), the header SHALL show an explicit placeholder instead of failing.

#### Scenario: Repository with remote
- **WHEN** the working directory has remote `origin` and branch `master`
- **THEN** the page header shows the origin URL and `master`

#### Scenario: No remote configured
- **WHEN** the working directory is a git repository with no remote
- **THEN** the header shows a "no remote" placeholder and the branch name, and the session proceeds normally

### Requirement: Live goal, plan, and step display
The status page SHALL display the planning Goal, the Plan, and the sequence of workflow steps the orchestrator has emitted, and SHALL update in real time as the session progresses without a manual browser refresh. Progress messages written to the terminal during planning SHALL also appear as step entries on the page, including a visible indication when the session is waiting for terminal input.

#### Scenario: Goal appears at session start
- **WHEN** the orchestrator writes `GOAL.md`
- **THEN** the page shows the goal text within the next update event

#### Scenario: Steps stream live
- **WHEN** the orchestrator emits a progress message (e.g., "writing planning goal")
- **THEN** a timestamped step entry appears on the open page without reloading

#### Scenario: Plan appears when written
- **WHEN** the AI tool produces `PLAN.md`
- **THEN** the page's Plan section shows the plan contents

#### Scenario: Waiting for terminal input
- **WHEN** the session is prompting the user with clarifying questions on the terminal
- **THEN** the page shows a step entry indicating input is awaited on the terminal

#### Scenario: Late-joining browser
- **WHEN** a browser opens the page mid-session
- **THEN** it immediately renders the current goal, plan (if any), and all steps emitted so far

### Requirement: Planning completion banner
When the planning phase ends, the status page SHALL display a clear banner indicating planning has completed, including the planning phase start time, end time, and total duration. The banner SHALL distinguish successful completion from failure. In interactive plan-only mode the process SHALL keep serving the page after completion until the user dismisses the session (e.g., presses Enter or interrupts), so the banner remains viewable.

#### Scenario: Successful completion
- **WHEN** planning finishes with an accepted `PLAN.md` and `STEPS.md`
- **THEN** the page shows a completion banner with start time, end time, and duration, and the terminal states the server is still available until dismissed

#### Scenario: Failed planning
- **WHEN** planning ends in failure (e.g., budget exhausted or tool made no progress)
- **THEN** the page shows a banner marked as failed with the same start/end/duration details

#### Scenario: Timing accuracy
- **WHEN** planning starts at T0 and ends at T1 per the injected clock
- **THEN** the banner shows T0 as start, T1 as end, and T1−T0 as duration

---
name: plan-panel
description: Triage-routed planning pipeline. An assessor classifies the request as simple or complex; simple requests get one minimal planner, complex requests get a scout, three lens-diverse alternative planners, and a judge that picks or merges them into one final plan. Use when the user asks to plan something with /plan-panel, or wants alternatives-and-judge planning.
---

# Plan Panel

Orchestrate planning through a triage gate. You (the main thread) run this
pipeline with the Agent tool using the custom agents defined in
`.claude/agents/`. All agent calls below use `run_in_background: false` except
the three alternative planners, which run concurrently.

The request being planned is the skill's args, or if empty, the user's most
recent planning request in the conversation.

## Budget flags

Args may include two optional budget flags (strip them from the request text
before passing it to agents):

- `--max-step-passes <n>` — how many planning passes the routed planner may
  take. A pass is one complete plan attempt; no revision loops beyond this.
- `--max-duration <time>` — wall-clock ceiling for the planning pipeline
  (e.g. `15m`). Note the start time when the pipeline begins; if the ceiling is
  hit, stop spawning agents, deliver the best plan produced so far, and tell
  the user the budget expired.

Defaults when the assessor routes SIMPLE and a flag was not given:
`--max-step-passes 1` and `--max-duration 15m`. The complex route has no
default budgets — only explicitly passed flags apply.

Include the active budgets in the routed planner's prompt so it paces itself.

## Pipeline

```
request → plan-assessor ─ simple ─→ plan-simple ──────────────→ final plan
                    │                    │ (escalate)
                    └─ complex ─→ plan-scout → 3× plan-alternative → plan-judge → final plan
```

### Step 1 — Assess

Spawn `plan-assessor` (synchronous) with the request verbatim. It returns
`{route, reason, blast_radius}`. Tell the user the verdict in one line
("Assessor: simple — single-file fix" / "Assessor: complex — touches shared
schema"). Route accordingly. If its output is not parseable JSON, treat as
complex (the assessor's own tie-break rule).

### Step 2a — Simple path

Spawn `plan-simple` (synchronous) with the request plus the assessor's
blast_radius note. Two outcomes:

- A plan: that is the final plan. Go to Step 4.
- `{escalate: true, reason}`: tell the user in one line, then run the complex
  path (Step 2b) passing the escalation reason along to the scout. The simple
  route's default budgets do not carry over — on the complex path only
  explicitly passed flags apply.

### Step 2b — Complex path

1. Spawn `plan-scout` (synchronous) with the request (plus escalation reason if
   any). Receive the context brief.
2. Spawn THREE `plan-alternative` agents in one message (concurrent,
   `subagent_type: "plan-alternative"`), each prompt containing:
   - the request, verbatim
   - the full scout brief
   - its assigned lens: `MVP-first`, `risk-first`, `performance-first`
     (if the user asked for different lenses or a different N, honor that)
3. Spawn `plan-judge` (synchronous) with the request, the scout brief, and all
   plans that came back. If a planner returned null/failed, judge the survivors;
   note the loss to the user. If ALL planners failed, stop and report — do not
   invent a plan yourself.

### Step 4 — Deliver

Present the final plan to the user as the deliverable of this turn. If the
session is in plan mode, present it via ExitPlanMode for approval. Do not start
implementing — planning and implementing are separate approvals.

## Rules

- Never skip the assessor, and never overrule its route yourself — the
  escalation hatch in `plan-simple` is the only correction path.
- Do not add your own exploration between steps; the agents own their phases.
- Relay agent failures honestly; a missing planner is reported, not silently
  absorbed.

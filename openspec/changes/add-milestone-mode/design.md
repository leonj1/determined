# Design

`MilestoneOrchestrator` owns persisted milestone state and delegates each approved milestone's `STEPS.md` to the existing `Orchestrator`. Read-only gate verdicts and worker divergence can trigger a forward-only bounded replan.

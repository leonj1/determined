package models

// MilestoneProgress is the currently visible phase of a milestone run.
type MilestoneProgress struct {
	Current  int    `json:"current"`
	Total    int    `json:"total"`
	Revision int    `json:"revision"`
	Phase    string `json:"phase"`
}

// MilestoneStatus is the persisted gate state for one milestone.
type MilestoneStatus struct {
	IntentChecked    bool   `json:"intentChecked"`
	ApprovedRevision int    `json:"approvedRevision"`
	Verified         bool   `json:"verified"`
	IntentRetries    int    `json:"intentRetries"`
	DefinitionHash   string `json:"definitionHash,omitempty"`
}

// MilestoneState is the resumable outer-loop checkpoint.
type MilestoneState struct {
	PlanRevision     int                        `json:"planRevision"`
	CurrentMilestone int                        `json:"currentMilestone"`
	Milestones       map[string]MilestoneStatus `json:"milestones"`
}

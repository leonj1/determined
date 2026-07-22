package models

import "strings"

// StallDecision is the user's verdict when an execute run stalls (the worker
// and reviewers ping-pong on one step until MaxStalledIterations hits) and the
// interactive status page asks a watching user to break the tie.
type StallDecision string

const (
	// StallDecisionNone is the zero value: no verdict recorded yet.
	StallDecisionNone StallDecision = ""
	// StallDecisionAcceptWorker trusts the worker: check the stalled step and
	// resume, stopping the reviewers' objections to it.
	StallDecisionAcceptWorker StallDecision = "accept-worker"
	// StallDecisionHoldReviewer keeps the stalled step unchecked and resumes,
	// giving any pending background verification another iteration to finish.
	StallDecisionHoldReviewer StallDecision = "hold-reviewer"
	// StallDecisionOther carries a freehand instruction the run queues as
	// guidance before resuming.
	StallDecisionOther StallDecision = "other"
	// StallDecisionCancel leaves the run stopped, preserving the pre-modal
	// OutcomeStalled behavior.
	StallDecisionCancel StallDecision = "cancel"
)

// StallOption is one side-by-side recommendation the tiebreak modal offers.
// Decision is the verdict submitting this option yields, Title is its short
// heading, and Synopsis is the succinct case for choosing it, phrased for the
// specific stalled step so the two options read as a real side-by-side choice
// rather than generic "worker vs hold" labels.
type StallOption struct {
	Decision StallDecision `json:"decision"`
	Title    string        `json:"title"`
	Synopsis string        `json:"synopsis"`
}

// StallPrompt is everything the run hands the tiebreak modal: the stalled
// step's title and the two recommendations to weigh side by side. Options
// always holds exactly the accept-worker and hold-reviewer choices, in that
// order; the modal renders the free-form field and Cancel separately.
type StallPrompt struct {
	StepTitle string        `json:"stepTitle"`
	Options   []StallOption `json:"options"`
}

// StallGuidance is the resolved verdict the status page returns to the run.
// Comment is the freehand instruction and is meaningful only for
// StallDecisionOther.
type StallGuidance struct {
	Decision StallDecision `json:"decision"`
	Comment  string        `json:"comment"`
}

// Valid reports whether the guidance names a known decision and, for Other,
// carries a non-blank comment. StallDecisionNone is never a valid submission.
func (g StallGuidance) Valid() bool {
	switch g.Decision {
	case StallDecisionAcceptWorker, StallDecisionHoldReviewer, StallDecisionCancel:
		return true
	case StallDecisionOther:
		return strings.TrimSpace(g.Comment) != ""
	}
	return false
}

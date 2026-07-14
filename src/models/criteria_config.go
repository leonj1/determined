package models

import "time"

// CriteriaConfig holds everything one attended criteria session needs. The
// user describes a journey, the tool proposes a BDD test via DraftFile from
// the description and revision notes in RequestFile, and every accepted test
// accumulates in CriteriaFile for planning and the final audit to enforce.
type CriteriaConfig struct {
	Invocation Invocation    // drafts or revises one BDD test (print mode)
	Budget     time.Duration // wall-clock budget; 0 means unlimited

	CriteriaFile string // accepted BDD tests (CRITERIA.md)
	RequestFile  string // journey description plus revision notes (CRITERIA_REQUEST.md)
	DraftFile    string // the tool's current proposal (CRITERIA_DRAFT.md)
}

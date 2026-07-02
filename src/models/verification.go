package models

// VerificationStatus is the verifier's decision about a completed step.
type VerificationStatus int

const (
	// VerificationFailed means changes should be repaired before committing.
	VerificationFailed VerificationStatus = iota
	// VerificationPassed means changes can be committed.
	VerificationPassed
)

// VerificationResult records the verifier's decision and feedback.
type VerificationResult struct {
	Status   VerificationStatus
	Feedback string
}

// Passed reports whether the verifier approved the changes.
func (r VerificationResult) Passed() bool {
	return r.Status == VerificationPassed
}

package services

import (
	"crypto/sha256"
	"determined/src/models"
	"encoding/json"
	"fmt"
	"strconv"
)

// NewMilestoneState seeds an unapproved state from a milestone document.
func NewMilestoneState(doc MilestoneDocument) models.MilestoneState {
	s := models.MilestoneState{PlanRevision: 1, Milestones: map[string]models.MilestoneStatus{}}
	for _, m := range doc.Milestones {
		s.Milestones[strconv.Itoa(m.Number)] = models.MilestoneStatus{DefinitionHash: milestoneDefinitionHash(m)}
	}
	if m, ok := doc.Next(-1); ok {
		s.CurrentMilestone = m.Number
	}
	return s
}

// CanExecute reports whether the milestone and all preceding milestones passed their gates.
func CanExecute(s models.MilestoneState, n int) bool {
	v, ok := s.Milestones[strconv.Itoa(n)]
	if !ok || v.Verified || !v.IntentChecked || v.ApprovedRevision != s.PlanRevision {
		return false
	}
	for k, p := range s.Milestones {
		i, err := strconv.Atoi(k)
		if err != nil {
			return false
		}
		if i < n && !p.Verified {
			return false
		}
	}
	return true
}

// ApproveIntent returns a copy approved for the current plan revision.
func ApproveIntent(s models.MilestoneState, n int) models.MilestoneState {
	s.Milestones = copyMilestoneStatuses(s.Milestones)
	v := s.Milestones[strconv.Itoa(n)]
	v.IntentChecked = true
	v.ApprovedRevision = s.PlanRevision
	s.Milestones[strconv.Itoa(n)] = v
	return s
}

// MarkVerified returns a copy with the milestone verification recorded.
func MarkVerified(s models.MilestoneState, n int) models.MilestoneState {
	s.Milestones = copyMilestoneStatuses(s.Milestones)
	v := s.Milestones[strconv.Itoa(n)]
	v.Verified = true
	s.Milestones[strconv.Itoa(n)] = v
	return s
}

// Replan returns a new revision with current and later approvals invalidated.
func Replan(s models.MilestoneState, from int) models.MilestoneState {
	s.Milestones = copyMilestoneStatuses(s.Milestones)
	s.PlanRevision++
	for k, v := range s.Milestones {
		n, err := strconv.Atoi(k)
		if err != nil {
			continue
		}
		if n >= from {
			v.IntentChecked = false
			v.ApprovedRevision = 0
			v.Verified = false
			v.IntentRetries = 0
			s.Milestones[k] = v
		}
	}
	return s
}

// NextMilestone returns the first unverified milestone in document order.
func NextMilestone(s models.MilestoneState, d MilestoneDocument) (int, bool) {
	for _, m := range d.Milestones {
		if !s.Milestones[strconv.Itoa(m.Number)].Verified {
			return m.Number, true
		}
	}
	return 0, false
}

// AllVerified reports whether the non-empty state has no unfinished milestone.
func AllVerified(s models.MilestoneState) bool {
	if len(s.Milestones) == 0 {
		return false
	}
	for _, v := range s.Milestones {
		if !v.Verified {
			return false
		}
	}
	return true
}

// LoadMilestoneState reads a persisted checkpoint when it exists.
func LoadMilestoneState(f FileStore, path string) (models.MilestoneState, bool, error) {
	if !f.Exists(path) {
		return models.MilestoneState{}, false, nil
	}
	c, e := f.Read(path)
	if e != nil {
		return models.MilestoneState{}, true, e
	}
	var s models.MilestoneState
	e = json.Unmarshal([]byte(c), &s)
	return s, true, e
}

// SaveMilestoneState atomically exposes the JSON representation through FileStore.
func SaveMilestoneState(f FileStore, path string, s models.MilestoneState) error {
	b, e := json.MarshalIndent(s, "", "  ")
	if e != nil {
		return e
	}
	if e = f.Write(path, string(b)+"\n"); e != nil {
		return fmt.Errorf("save milestone state: %w", e)
	}
	return nil
}

// copyMilestoneStatuses isolates state transitions from their input map.
func copyMilestoneStatuses(source map[string]models.MilestoneStatus) map[string]models.MilestoneStatus {
	result := make(map[string]models.MilestoneStatus, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

// milestoneDefinitionHash fingerprints every field that controls execution.
func milestoneDefinitionHash(m Milestone) string {
	definition := fmt.Sprintf("%d\x00%s\x00%s\x00%s\x00%s\x00%s", m.Number, m.Name, m.Goal, m.WorkingState, m.RisksRetired, m.DependsOn)
	return fmt.Sprintf("%x", sha256.Sum256([]byte(definition)))
}

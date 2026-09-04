package services

import (
	"determined/src/models"
	"encoding/json"
	"fmt"
	"strconv"
)

func NewMilestoneState(doc MilestoneDocument) models.MilestoneState {
	s := models.MilestoneState{PlanRevision: 1, Milestones: map[string]models.MilestoneStatus{}}
	for _, m := range doc.Milestones {
		s.Milestones[strconv.Itoa(m.Number)] = models.MilestoneStatus{}
	}
	if m, ok := doc.Next(-1); ok {
		s.CurrentMilestone = m.Number
	}
	return s
}
func CanExecute(s models.MilestoneState, n int) bool {
	v, ok := s.Milestones[strconv.Itoa(n)]
	if !ok || v.Verified || !v.IntentChecked || v.ApprovedRevision != s.PlanRevision {
		return false
	}
	for k, p := range s.Milestones {
		i, _ := strconv.Atoi(k)
		if i < n && !p.Verified {
			return false
		}
	}
	return true
}
func ApproveIntent(s *models.MilestoneState, n int) {
	v := s.Milestones[strconv.Itoa(n)]
	v.IntentChecked = true
	v.ApprovedRevision = s.PlanRevision
	s.Milestones[strconv.Itoa(n)] = v
}
func MarkVerified(s *models.MilestoneState, n int) {
	v := s.Milestones[strconv.Itoa(n)]
	v.Verified = true
	s.Milestones[strconv.Itoa(n)] = v
}
func Replan(s *models.MilestoneState, from int) {
	s.PlanRevision++
	for k, v := range s.Milestones {
		n, _ := strconv.Atoi(k)
		if n >= from {
			v.IntentChecked = false
			v.ApprovedRevision = 0
			v.Verified = false
			v.IntentRetries = 0
			s.Milestones[k] = v
		}
	}
}
func NextMilestone(s models.MilestoneState, d MilestoneDocument) (int, bool) {
	for _, m := range d.Milestones {
		if !s.Milestones[strconv.Itoa(m.Number)].Verified {
			return m.Number, true
		}
	}
	return 0, false
}
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

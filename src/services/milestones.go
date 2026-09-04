package services

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type Milestone struct {
	Number                                            int
	Name, Goal, WorkingState, RisksRetired, DependsOn string
}

type MilestoneDocument struct {
	Milestones []Milestone
	Trailer    string
	raw        string
}

var milestoneHeading = regexp.MustCompile(`^## Milestone ([0-9]+):\s*(.*)$`)

func ParseMilestones(content string) (MilestoneDocument, error) {
	starts := milestoneStarts(content)
	if len(starts) == 0 {
		return MilestoneDocument{}, fmt.Errorf("no milestones found")
	}
	doc := MilestoneDocument{raw: content}
	seen := map[int]bool{}
	for i, start := range starts {
		end := len(content)
		if i+1 < len(starts) {
			end = starts[i+1]
		}
		block, trailerAt := milestoneBlock(content[start:end])
		if trailerAt >= 0 {
			end = start + trailerAt
			block = content[start:end]
			doc.Trailer = content[end:]
			starts = starts[:i+1]
		}
		m, err := parseMilestone(block)
		if err != nil {
			return MilestoneDocument{}, err
		}
		if seen[m.Number] {
			return MilestoneDocument{}, fmt.Errorf("duplicate milestone %d", m.Number)
		}
		seen[m.Number] = true
		doc.Milestones = append(doc.Milestones, m)
		if trailerAt >= 0 {
			break
		}
	}
	return doc, nil
}

func milestoneStarts(content string) []int {
	var starts []int
	offset, fenced := 0, false
	for _, line := range strings.SplitAfter(content, "\n") {
		trimmed := strings.TrimSpace(strings.TrimSuffix(line, "\n"))
		if strings.HasPrefix(trimmed, "```") {
			fenced = !fenced
		} else if !fenced && milestoneHeading.MatchString(trimmed) {
			starts = append(starts, offset)
		}
		offset += len(line)
	}
	return starts
}

func milestoneBlock(block string) (string, int) {
	lines, offset := strings.SplitAfter(block, "\n"), 0
	for i, line := range lines {
		if i > 0 && strings.HasPrefix(strings.TrimSpace(line), "## ") {
			return block[:offset], offset
		}
		offset += len(line)
	}
	return block, -1
}

func parseMilestone(block string) (Milestone, error) {
	lines := strings.Split(block, "\n")
	match := milestoneHeading.FindStringSubmatch(strings.TrimSpace(lines[0]))
	n, _ := strconv.Atoi(match[1])
	m := Milestone{Number: n, Name: match[2]}
	for _, line := range lines[1:] {
		value := func(prefix string) string { return strings.TrimSpace(strings.TrimPrefix(line, prefix)) }
		switch {
		case strings.HasPrefix(line, "Goal:"):
			m.Goal = value("Goal:")
		case strings.HasPrefix(line, "Working state:"):
			m.WorkingState = value("Working state:")
		case strings.HasPrefix(line, "Risks retired:"):
			m.RisksRetired = value("Risks retired:")
		case strings.HasPrefix(line, "Depends on:"):
			m.DependsOn = value("Depends on:")
		}
	}
	if m.Goal == "" {
		return Milestone{}, fmt.Errorf("milestone %d missing Goal", n)
	}
	return m, nil
}

func (d MilestoneDocument) Render() string {
	if d.raw != "" {
		return d.raw
	}
	var b strings.Builder
	b.WriteString("# Milestones\n\n")
	for _, m := range d.Milestones {
		fmt.Fprintf(&b, "## Milestone %d: %s\nGoal: %s\nWorking state: %s\nRisks retired: %s\nDepends on: %s\n\n", m.Number, m.Name, m.Goal, m.WorkingState, m.RisksRetired, m.DependsOn)
	}
	return b.String() + d.Trailer
}

func (d MilestoneDocument) Find(n int) (Milestone, bool) {
	for _, m := range d.Milestones {
		if m.Number == n {
			return m, true
		}
	}
	return Milestone{}, false
}
func (d MilestoneDocument) Next(after int) (Milestone, bool) {
	ms := append([]Milestone(nil), d.Milestones...)
	sort.Slice(ms, func(i, j int) bool { return ms[i].Number < ms[j].Number })
	for _, m := range ms {
		if m.Number > after {
			return m, true
		}
	}
	return Milestone{}, false
}

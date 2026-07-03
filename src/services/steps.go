package services

import "strings"

// Step is one checkbox item parsed from a STEPS.md file.
type Step struct {
	// Text is the step's description without the checkbox marker, with any
	// indented continuation lines folded in.
	Text string
	// DoneWhen is the acceptance criterion from the step's "Done when:"
	// line, empty when the step has none.
	DoneWhen string
	// Completed reports whether the checkbox was checked ("[x]").
	Completed bool
}

// doneWhenPrefix marks a step's acceptance-criterion line, per the format the
// planning prompts enforce.
const doneWhenPrefix = "Done when:"

// ParseSteps parses checkbox-format STEPS.md content into its steps. The
// protocol asks the planning tool for one `- [ ]` item per step, each ending
// with an indented `Done when:` line stating the acceptance criterion. Lines
// that are not checkbox items or part of one (headings, blank lines, prose)
// are ignored, so surrounding chatter still parses cleanly.
func ParseSteps(content string) []Step {
	var steps []Step
	var cur *Step
	for _, line := range strings.Split(content, "\n") {
		if step, ok := checkboxItem(line); ok {
			steps = append(steps, step)
			cur = &steps[len(steps)-1]
			continue
		}
		if cur == nil {
			continue
		}
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "":
			cur = nil // a blank line ends the current item
		case hasFoldPrefix(trimmed, doneWhenPrefix):
			cur.DoneWhen = strings.TrimSpace(trimmed[len(doneWhenPrefix):])
		case isIndented(line):
			cur.Text += " " + trimmed
		default:
			cur = nil // unindented prose or a heading is not part of the item
		}
	}
	return steps
}

// NextIncompleteStep returns the first unchecked step and whether one exists.
func NextIncompleteStep(steps []Step) (Step, bool) {
	for _, s := range steps {
		if !s.Completed {
			return s, true
		}
	}
	return Step{}, false
}

// AllStepsComplete reports whether the list is non-empty and every step is
// checked. An empty or unparseable STEPS.md is deliberately not "complete":
// treating it as done would let a missing or malformed file end a run as
// success.
func AllStepsComplete(steps []Step) bool {
	if len(steps) == 0 {
		return false
	}
	for _, s := range steps {
		if !s.Completed {
			return false
		}
	}
	return true
}

// checkboxItem parses a markdown checkbox list item ("- [ ] text" or
// "- [x] text"; "*" bullets are accepted too) and reports whether the line was
// one. Bullets without a checkbox, or with an unrecognized mark, are not items.
func checkboxItem(line string) (Step, bool) {
	s := strings.TrimSpace(line)
	rest, ok := strings.CutPrefix(s, "- ")
	if !ok {
		rest, ok = strings.CutPrefix(s, "* ")
	}
	if !ok || len(rest) < 3 || rest[0] != '[' || rest[2] != ']' {
		return Step{}, false
	}
	var completed bool
	switch rest[1] {
	case ' ':
	case 'x', 'X':
		completed = true
	default:
		return Step{}, false
	}
	return Step{Text: strings.TrimSpace(rest[3:]), Completed: completed}, true
}

// hasFoldPrefix reports whether s starts with prefix, ignoring case.
func hasFoldPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}

// isIndented reports whether the line starts with whitespace, marking it a
// continuation of the current list item.
func isIndented(line string) bool {
	return line != "" && (line[0] == ' ' || line[0] == '\t')
}

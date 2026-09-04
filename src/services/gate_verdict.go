package services

import "strings"

// GateVerdict is the structured result emitted by a read-only milestone gate.
type GateVerdict struct{ Token, Rationale, Guidance string }

// ParseGateVerdict returns the first accepted verdict and its explanatory lines.
func ParseGateVerdict(output string, accepted ...string) (GateVerdict, bool) {
	allowed := map[string]bool{}
	for _, v := range accepted {
		allowed[strings.ToUpper(v)] = true
	}
	g := GateVerdict{}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case g.Token == "" && hasFoldPrefix(line, "VERDICT:"):
			t := strings.ToUpper(strings.TrimSpace(line[len("VERDICT:"):]))
			if allowed[t] {
				g.Token = t
			}
		case g.Rationale == "" && hasFoldPrefix(line, "RATIONALE:"):
			g.Rationale = strings.TrimSpace(line[len("RATIONALE:"):])
		case g.Guidance == "" && hasFoldPrefix(line, "GUIDANCE:"):
			g.Guidance = strings.TrimSpace(line[len("GUIDANCE:"):])
		}
	}
	return g, g.Token != ""
}

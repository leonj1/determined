package models

import "time"

// Invocation is a single AI-coding-tool command the orchestrator runs.
type Invocation struct {
	Binary string
	Args   []string
}

// Config holds everything one orchestrator run needs.
type Config struct {
	StopFile  string
	PlanFile  string // must exist at startup; execute mode refuses to run without a plan
	StepsFile string
	Tool      Tool          // builds each iteration's invocation from the injected prompt
	Budget    time.Duration // wall-clock budget; 0 means unlimited
}

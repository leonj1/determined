package models

import "time"

// Invocation is a single AI-coding-tool command the orchestrator runs.
type Invocation struct {
	Binary string
	Args   []string
}

// Config holds everything one orchestrator run needs.
type Config struct {
	StopFile   string
	StepsFile  string
	Invocation Invocation
	Budget     time.Duration // wall-clock budget; 0 means unlimited
}

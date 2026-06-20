package models

import "fmt"

// Tool builds the command invocation for a specific AI coding CLI. It is pure
// construction — given the prompt, it returns the Invocation to run.
type Tool interface {
	Name() string
	Invocation(prompt string) Invocation
}

// DroidTool runs the Factory droid CLI: `droid exec "<prompt>" --auto <level>`.
// The autonomy level is required for unattended runs.
type DroidTool struct {
	Auto string
}

func (DroidTool) Name() string { return "droid" }

func (t DroidTool) Invocation(prompt string) Invocation {
	return Invocation{Binary: "droid", Args: []string{"exec", prompt, "--auto", t.Auto}}
}

// PiTool runs the pi CLI: `pi -p "<prompt>"`.
type PiTool struct{}

func (PiTool) Name() string { return "pi" }

func (PiTool) Invocation(prompt string) Invocation {
	return Invocation{Binary: "pi", Args: []string{"-p", prompt}}
}

// ClaudeTool runs the Claude CLI in print mode: `claude -p "<prompt>"`.
type ClaudeTool struct{}

func (ClaudeTool) Name() string { return "claude" }

func (ClaudeTool) Invocation(prompt string) Invocation {
	return Invocation{Binary: "claude", Args: []string{"-p", prompt}}
}

// SelectTool returns the Tool named by name. auto is applied only to droid.
func SelectTool(name string, auto string) (Tool, error) {
	switch name {
	case "droid":
		return DroidTool{Auto: auto}, nil
	case "pi":
		return PiTool{}, nil
	case "claude":
		return ClaudeTool{}, nil
	default:
		return nil, fmt.Errorf("unknown tool %q (want droid, pi, or claude)", name)
	}
}

package models

import "fmt"

// Tool builds the command invocation for a specific AI coding CLI. It is pure
// construction — given the prompt, it returns the Invocation to run.
type Tool interface {
	Name() string
	Invocation(prompt string) Invocation
}

// droidAutonomy is the autonomy level droid runs at. Unattended runs always
// use "high"; it is not user-configurable.
const droidAutonomy = "high"

// DroidTool runs the Factory droid CLI: `droid exec "<prompt>" --auto high`.
// The autonomy level is fixed for unattended runs.
type DroidTool struct{}

func (DroidTool) Name() string { return "droid" }

func (DroidTool) Invocation(prompt string) Invocation {
	return Invocation{Binary: "droid", Args: []string{"exec", prompt, "--auto", droidAutonomy}}
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

// SelectTool returns the Tool named by name.
func SelectTool(name string) (Tool, error) {
	switch name {
	case "droid":
		return DroidTool{}, nil
	case "pi":
		return PiTool{}, nil
	case "claude":
		return ClaudeTool{}, nil
	default:
		return nil, fmt.Errorf("unknown tool %q (want droid, pi, or claude)", name)
	}
}

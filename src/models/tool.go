package models

import "fmt"

// ToolName identifies one supported AI coding CLI.
type ToolName string

const (
	ToolNameDroid  ToolName = "droid"
	ToolNamePi     ToolName = "pi"
	ToolNameClaude ToolName = "claude"
)

// ModelID is an optional model identifier or alias passed to CLIs that support
// model selection.
type ModelID string

func (m ModelID) Empty() bool { return m == "" }

// ToolOptions contains optional CLI selection settings.
type ToolOptions struct {
	Model ModelID
}

// ToolIdentity names the AI CLI and the model it runs, for display on the
// interactive status page. An empty Model means the CLI's own default model.
type ToolIdentity struct {
	Name  ToolName `json:"name"`
	Model ModelID  `json:"model"`
}

// Tool builds the command invocation for a specific AI coding CLI. It is pure
// construction — given the prompt, it returns the Invocation to run.
type Tool interface {
	Name() string
	Identity() ToolIdentity
	Invocation(prompt string) Invocation
}

// droidAutonomy is the autonomy level droid runs at. Unattended runs always
// use "high"; it is not user-configurable.
const droidAutonomy = "high"

// DroidTool runs the Factory droid CLI: `droid exec "<prompt>" --auto high`.
// The autonomy level is fixed for unattended runs.
type DroidTool struct {
	Model ModelID
}

func (DroidTool) Name() string { return "droid" }

func (t DroidTool) Identity() ToolIdentity {
	return ToolIdentity{Name: ToolNameDroid, Model: t.Model}
}

func (t DroidTool) Invocation(prompt string) Invocation {
	args := []string{"exec", prompt, "--auto", droidAutonomy}
	return Invocation{Binary: "droid", Args: withModel(args, t.Model)}
}

// PiTool runs the pi CLI: `pi -p "<prompt>"`.
type PiTool struct{}

func (PiTool) Name() string { return "pi" }

func (PiTool) Identity() ToolIdentity { return ToolIdentity{Name: ToolNamePi} }

func (PiTool) Invocation(prompt string) Invocation {
	return Invocation{Binary: "pi", Args: []string{"-p", prompt}}
}

// claudePermissionMode is the permission mode claude runs at. Print-mode runs
// cannot answer interactive permission prompts, so unattended runs always use
// "acceptEdits"; it is not user-configurable.
const claudePermissionMode = "acceptEdits"

// ClaudeTool runs the Claude CLI in print mode:
// `claude -p "<prompt>" --permission-mode acceptEdits`.
type ClaudeTool struct {
	Model ModelID
}

func (ClaudeTool) Name() string { return "claude" }

func (t ClaudeTool) Identity() ToolIdentity {
	return ToolIdentity{Name: ToolNameClaude, Model: t.Model}
}

func (t ClaudeTool) Invocation(prompt string) Invocation {
	args := []string{"-p", prompt, "--permission-mode", claudePermissionMode}
	return Invocation{Binary: "claude", Args: withModel(args, t.Model)}
}

// SelectTool returns the Tool named by name.
func SelectTool(name ToolName, options ToolOptions) (Tool, error) {
	switch name {
	case ToolNameDroid:
		return DroidTool{Model: options.Model}, nil
	case ToolNamePi:
		if !options.Model.Empty() {
			return nil, fmt.Errorf("--model is only supported with droid or claude (selected pi)")
		}
		return PiTool{}, nil
	case ToolNameClaude:
		return ClaudeTool{Model: options.Model}, nil
	default:
		return nil, fmt.Errorf("unknown tool %q (want droid, pi, or claude)", name)
	}
}

func withModel(args []string, model ModelID) []string {
	if model.Empty() {
		return args
	}
	return append(args, "--model", string(model))
}

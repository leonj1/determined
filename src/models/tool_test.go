package models_test

import (
	"strings"
	"testing"

	"determined/src/models"
)

// render reconstructs the shell-ish command a user would see executed.
func render(inv models.Invocation) string {
	parts := []string{inv.Binary}
	for _, a := range inv.Args {
		if strings.Contains(a, " ") {
			a = `"` + a + `"`
		}
		parts = append(parts, a)
	}
	return strings.Join(parts, " ")
}

func TestSelectedToolRunsTheRightCommand(t *testing.T) {
	cases := []struct {
		tool string
		want string
	}{
		{"droid", `droid exec "do the work" --auto high`},
		{"pi", `pi -p "do the work"`},
		{"claude", `claude -p "do the work" --permission-mode acceptEdits`},
	}
	for _, c := range cases {
		tool, err := models.SelectTool(models.ToolName(c.tool), models.ToolOptions{})
		if err != nil {
			t.Fatalf("%s should be supported: %v", c.tool, err)
		}
		if tool.Name() != c.tool {
			t.Errorf("expected tool name %q, got %q", c.tool, tool.Name())
		}
		if got := render(tool.Invocation("do the work")); got != c.want {
			t.Errorf("%s: expected to run %q, got %q", c.tool, c.want, got)
		}
	}
}

func TestSelectedDroidAndClaudeCanOverrideTheModel(t *testing.T) {
	cases := []struct {
		tool string
		want string
	}{
		{"droid", `droid exec "do the work" --auto high --model claude-opus-4-7`},
		{"claude", `claude -p "do the work" --permission-mode acceptEdits --model opus`},
	}
	modelsByTool := map[string]models.ModelID{
		"droid":  "claude-opus-4-7",
		"claude": "opus",
	}
	for _, c := range cases {
		tool, err := models.SelectTool(
			models.ToolName(c.tool),
			models.ToolOptions{Model: modelsByTool[c.tool]},
		)
		if err != nil {
			t.Fatalf("%s should accept a model override: %v", c.tool, err)
		}
		if got := render(tool.Invocation("do the work")); got != c.want {
			t.Errorf("%s: expected to run %q, got %q", c.tool, c.want, got)
		}
	}
}

func TestUnsupportedToolIsRejected(t *testing.T) {
	_, err := models.SelectTool("gpt", models.ToolOptions{})
	if err == nil {
		t.Fatal("expected an error when selecting an unsupported tool")
	}
}

func TestPiRejectsModelOverride(t *testing.T) {
	_, err := models.SelectTool("pi", models.ToolOptions{Model: "opus"})
	if err == nil {
		t.Fatal("expected pi to reject model overrides")
	}
}

func TestToolIdentityCarriesNameAndModel(t *testing.T) {
	cases := []struct {
		tool  models.ToolName
		model models.ModelID
		want  models.ToolIdentity
	}{
		{"droid", "claude-opus-4-7", models.ToolIdentity{Name: models.ToolNameDroid, Model: "claude-opus-4-7"}},
		{"claude", "opus", models.ToolIdentity{Name: models.ToolNameClaude, Model: "opus"}},
		{"claude", "", models.ToolIdentity{Name: models.ToolNameClaude}},
		{"pi", "", models.ToolIdentity{Name: models.ToolNamePi}},
	}
	for _, c := range cases {
		tool, err := models.SelectTool(c.tool, models.ToolOptions{Model: c.model})
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", c.tool, err)
		}
		if tool.Identity() != c.want {
			t.Errorf("%s: identity = %+v, want %+v", c.tool, tool.Identity(), c.want)
		}
	}
}

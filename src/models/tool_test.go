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
		auto string
		want string
	}{
		{"droid", "high", `droid exec "do the work" --auto high`},
		{"pi", "ignored", `pi -p "do the work"`},
		{"claude", "ignored", `claude -p "do the work"`},
	}
	for _, c := range cases {
		tool, err := models.SelectTool(c.tool, c.auto)
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

func TestUnsupportedToolIsRejected(t *testing.T) {
	_, err := models.SelectTool("gpt", "")
	if err == nil {
		t.Fatal("expected an error when selecting an unsupported tool")
	}
}

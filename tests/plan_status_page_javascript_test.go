package tests

import (
	"os/exec"
	"testing"
)

func TestPlanStatusPageJavaScript(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("node is required to test the interactive status page JavaScript")
	}
	command := exec.Command(node, "--test", "plan_status_page_test.js")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("status page JavaScript tests failed: %v\n%s", err, output)
	}
}

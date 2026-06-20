package clients

import (
	"context"
	"io"
	"os/exec"

	"determined/src/models"
)

// ExecCommandRunner runs invocations as real child processes.
type ExecCommandRunner struct{}

// NewExecCommandRunner constructs an ExecCommandRunner.
func NewExecCommandRunner() ExecCommandRunner { return ExecCommandRunner{} }

// Run streams the child's stdout and stderr to out and blocks until it exits.
// A cancelled context kills the child, surfacing as a non-nil error.
func (ExecCommandRunner) Run(ctx context.Context, inv models.Invocation, out io.Writer) error {
	cmd := exec.CommandContext(ctx, inv.Binary, inv.Args...)
	cmd.Stdout = out
	cmd.Stderr = out
	return cmd.Run()
}

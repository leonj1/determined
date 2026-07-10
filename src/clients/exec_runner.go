package clients

import (
	"context"
	"io"
	"os"
	"os/exec"

	"determined/src/models"
)

// ExecCommandRunner runs invocations as real child processes.
type ExecCommandRunner struct{}

// NewExecCommandRunner constructs an ExecCommandRunner.
func NewExecCommandRunner() ExecCommandRunner { return ExecCommandRunner{} }

// Run streams the child's stdout and stderr to out and blocks until it exits.
// A cancelled context kills the child, surfacing as a non-nil error. The
// invocation's Env entries are appended to the inherited environment.
func (ExecCommandRunner) Run(ctx context.Context, inv models.Invocation, out io.Writer) error {
	cmd := exec.CommandContext(ctx, inv.Binary, inv.Args...)
	if len(inv.Env) > 0 {
		cmd.Env = append(os.Environ(), inv.Env...)
	}
	cmd.Stdout = out
	cmd.Stderr = out
	return cmd.Run()
}

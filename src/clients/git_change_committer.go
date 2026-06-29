package clients

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
)

// GitRunner runs git subprocesses for GitChangeCommitter.
type GitRunner interface {
	Run(ctx context.Context, stdout, stderr io.Writer, name string, args ...string) error
}

// ExecGitRunner runs git subprocesses through the operating system.
type ExecGitRunner struct{}

// NewExecGitRunner constructs an ExecGitRunner.
func NewExecGitRunner() ExecGitRunner { return ExecGitRunner{} }

// Run executes a command with separate stdout and stderr writers.
func (ExecGitRunner) Run(ctx context.Context, stdout, stderr io.Writer, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

// GitChangeCommitter commits all repository changes with a fixed message.
type GitChangeCommitter struct {
	message string
	runner  GitRunner
}

// NewGitChangeCommitter constructs a GitChangeCommitter.
func NewGitChangeCommitter(message string, runner GitRunner) GitChangeCommitter {
	return GitChangeCommitter{message: message, runner: runner}
}

// Commit stages and commits every repository change. A clean worktree is a
// successful no-op.
func (c GitChangeCommitter) Commit(ctx context.Context, out io.Writer) error {
	changed, err := c.hasChanges(ctx)
	if err != nil {
		return err
	}
	if !changed {
		_, err := fmt.Fprintln(out, "No repository changes to commit")
		return err
	}
	if err := c.run(ctx, out, "git", "add", "-A"); err != nil {
		return err
	}
	return c.run(ctx, out, "git", "commit", "-m", c.message)
}

func (c GitChangeCommitter) hasChanges(ctx context.Context) (bool, error) {
	var out bytes.Buffer
	if err := c.runner.Run(ctx, &out, &out, "git", "status", "--porcelain"); err != nil {
		return false, fmt.Errorf("git status: %s: %w", out.String(), err)
	}
	return out.Len() > 0, nil
}

func (c GitChangeCommitter) run(ctx context.Context, out io.Writer, name string, args ...string) error {
	if err := c.runner.Run(ctx, out, out, name, args...); err != nil {
		return fmt.Errorf("%s %v: %w", name, args, err)
	}
	return nil
}

package clients

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
)

// GitChangeCommitter commits all repository changes with a fixed message.
type GitChangeCommitter struct {
	message string
}

// NewGitChangeCommitter constructs a GitChangeCommitter.
func NewGitChangeCommitter(message string) GitChangeCommitter {
	return GitChangeCommitter{message: message}
}

// Commit stages and commits every repository change. A clean worktree is a
// successful no-op.
func (c GitChangeCommitter) Commit(ctx context.Context, out io.Writer) error {
	changed, err := c.hasChanges(ctx)
	if err != nil {
		return err
	}
	if !changed {
		fmt.Fprintln(out, "No repository changes to commit")
		return nil
	}
	if err := c.run(ctx, out, "git", "add", "-A"); err != nil {
		return err
	}
	return c.run(ctx, out, "git", "commit", "-m", c.message)
}

func (c GitChangeCommitter) hasChanges(ctx context.Context) (bool, error) {
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", "status", "--porcelain")
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return false, fmt.Errorf("git status: %s: %w", out.String(), err)
	}
	return out.Len() > 0, nil
}

func (c GitChangeCommitter) run(ctx context.Context, out io.Writer, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %v: %w", name, args, err)
	}
	return nil
}

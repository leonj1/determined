package clients

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"

	"determined/src/models"
)

// ExecGitWorkspace performs branch-level git operations by shelling out to
// git in the working directory. It implements services.GitWorkspace.
type ExecGitWorkspace struct{}

// NewExecGitWorkspace constructs an ExecGitWorkspace.
func NewExecGitWorkspace() ExecGitWorkspace { return ExecGitWorkspace{} }

// CurrentBranch returns the checked-out branch name. A detached or unborn
// HEAD yields an error.
func (ExecGitWorkspace) CurrentBranch(ctx context.Context) (models.BranchName, error) {
	out, err := gitOutput(ctx, "rev-parse", "--abbrev-ref", "HEAD")
	return models.BranchName(out), err
}

// Head returns the SHA of the current commit.
func (ExecGitWorkspace) Head(ctx context.Context) (models.CommitSHA, error) {
	out, err := gitOutput(ctx, "rev-parse", "HEAD")
	return models.CommitSHA(out), err
}

// CreateBranch creates the named branch at HEAD and switches to it.
func (ExecGitWorkspace) CreateBranch(ctx context.Context, name models.BranchName) error {
	return gitRun(ctx, "checkout", "-b", string(name))
}

// Squash collapses every commit after base into one: a soft reset moves the
// branch back to base while keeping the accumulated tree staged, and a single
// commit records it under the given message.
func (ExecGitWorkspace) Squash(ctx context.Context, base models.CommitSHA, message string) error {
	if err := gitRun(ctx, "reset", "--soft", string(base)); err != nil {
		return err
	}
	return gitRun(ctx, "commit", "-m", message)
}

// gitOutput runs a git command and returns its trimmed stdout.
func gitOutput(ctx context.Context, args ...string) (string, error) {
	var out, errOut bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		return "", gitError(args, errOut.String(), err)
	}
	return strings.TrimSpace(out.String()), nil
}

// gitRun runs a git command for its effect only.
func gitRun(ctx context.Context, args ...string) error {
	var errOut bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		return gitError(args, errOut.String(), err)
	}
	return nil
}

// gitError wraps a failed git command with the command line and whatever git
// printed on stderr, so failures surface with their cause.
func gitError(args []string, stderr string, err error) error {
	detail := strings.TrimSpace(stderr)
	if detail == "" {
		return fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, detail)
}

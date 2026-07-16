package clients

import (
	"bytes"
	"context"
	"os/exec"
	"strings"

	"determined/src/models"
)

// GitContextReader reads the working directory's remote and branch by shelling
// out to git. Missing information becomes an explicit placeholder rather than
// an error: the status page must render in non-repositories too.
type GitContextReader struct{}

// NewGitContextReader constructs a GitContextReader.
func NewGitContextReader() GitContextReader { return GitContextReader{} }

// Read returns the origin remote URL and current branch, substituting
// "no remote" and "no branch" when git cannot supply them.
func (GitContextReader) Read(ctx context.Context) models.GitContext {
	return models.GitContext{
		Remote: gitValue(ctx, "no remote", "remote", "get-url", "origin"),
		Branch: gitValue(ctx, "no branch", "rev-parse", "--abbrev-ref", "HEAD"),
	}
}

func gitValue(ctx context.Context, placeholder string, args ...string) string {
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return placeholder
	}
	value := strings.TrimSpace(out.String())
	if value == "" {
		return placeholder
	}
	return value
}

package services

import (
	"context"
	"fmt"
	"io"
	"strings"

	"determined/src/models"
)

// GitWorkspace performs the branch-level git operations a run needs to keep
// its commits off a default branch. The real implementation is
// clients.ExecGitWorkspace.
type GitWorkspace interface {
	CurrentBranch(ctx context.Context) (models.BranchName, error)
	Head(ctx context.Context) (models.CommitSHA, error)
	CreateBranch(ctx context.Context, name models.BranchName) error
	Squash(ctx context.Context, base models.CommitSHA, message string) error
}

// BranchIsolation keeps a run's commits off the default branches. Begin moves
// the run onto a fresh branch when it would otherwise commit to main, master,
// or develop; Finish squashes everything that run committed into a single
// commit describing the change's intent.
type BranchIsolation struct {
	git      GitWorkspace
	files    FileStore
	clock    Clock
	terminal io.Writer
}

// NewBranchIsolation wires a BranchIsolation from its dependencies.
func NewBranchIsolation(git GitWorkspace, files FileStore, clock Clock, terminal io.Writer) *BranchIsolation {
	return &BranchIsolation{git: git, files: files, clock: clock, terminal: terminal}
}

// Begin creates and switches to a fresh run branch when the working directory
// is a git repository currently on a default branch, returning the state
// Finish needs. Outside a repository, on a non-default branch, or where the
// current branch cannot be read (an unborn or detached HEAD), there is nothing
// to protect and the zero state is returned.
func (b *BranchIsolation) Begin(ctx context.Context) (models.BranchState, error) {
	if !b.files.Exists(".git") {
		return models.BranchState{}, nil
	}
	current, err := b.git.CurrentBranch(ctx)
	if err != nil {
		return models.BranchState{}, nil
	}
	if !defaultBranch(current) {
		return models.BranchState{}, nil
	}
	return b.createRunBranch(ctx, current)
}

// createRunBranch records the commit the run starts from, then creates and
// switches to a timestamped branch off it.
func (b *BranchIsolation) createRunBranch(ctx context.Context, current models.BranchName) (models.BranchState, error) {
	base, err := b.git.Head(ctx)
	if err != nil {
		return models.BranchState{}, fmt.Errorf("read HEAD before branching off %s: %w", current, err)
	}
	name := models.BranchName("determined/run-" + b.clock.Now().Format("20060102-150405"))
	if err := b.git.CreateBranch(ctx, name); err != nil {
		return models.BranchState{}, fmt.Errorf("create branch %s off %s: %w", name, current, err)
	}
	fmt.Fprintf(b.terminal,
		"determined: created branch %s so run commits stay off %s\n", name, current)
	return models.BranchState{Created: true, Name: name, Base: base}, nil
}

// Finish squashes every commit the run made on its branch into a single
// commit describing the change's intent. A run that never branched, or one
// that branched but committed nothing, needs no squash.
func (b *BranchIsolation) Finish(ctx context.Context, state models.BranchState) error {
	if !state.Created {
		return nil
	}
	head, err := b.git.Head(ctx)
	if err != nil {
		return fmt.Errorf("read HEAD before squashing %s: %w", state.Name, err)
	}
	if head == state.Base {
		return nil
	}
	if err := b.git.Squash(ctx, state.Base, b.squashMessage(state)); err != nil {
		return fmt.Errorf("squash %s: %w", state.Name, err)
	}
	fmt.Fprintf(b.terminal,
		"determined: squashed run commits on %s into a single commit\n", state.Name)
	return nil
}

// squashMessage builds the squash commit's message: the change's intent as the
// subject, with the run branch recorded in the body.
func (b *BranchIsolation) squashMessage(state models.BranchState) string {
	return fmt.Sprintf("%s\n\nSquashed from determined run commits on %s.",
		b.intent(), state.Name)
}

// intent describes what the run set out to change, taken from the goal the
// user stated (GOAL.md) or, failing that, the plan's title (PLAN.md).
func (b *BranchIsolation) intent() string {
	for _, file := range []string{"GOAL.md", "PLAN.md"} {
		content, err := b.files.Read(file)
		if err != nil {
			continue
		}
		if line := firstContentLine(content); line != "" {
			return line
		}
	}
	return "determined: automated change"
}

// firstContentLine returns the first non-empty line with markdown heading
// markers stripped, or "" when no such line exists.
func firstContentLine(content string) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "#"))
		if line != "" {
			return line
		}
	}
	return ""
}

// defaultBranch reports whether name is one of the shared default branches a
// run must never commit to directly.
func defaultBranch(name models.BranchName) bool {
	switch name {
	case "main", "master", "develop":
		return true
	}
	return false
}

package services_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"determined/src/models"
	"determined/src/services"
)

// fakeGitWorkspace is a scripted, recording GitWorkspace.
type fakeGitWorkspace struct {
	branch    models.BranchName
	branchErr error
	head      models.CommitSHA
	headErr   error
	createErr error
	squashErr error
	created   []models.BranchName
	squashes  []squashCall
}

type squashCall struct {
	base    models.CommitSHA
	message string
}

func (g *fakeGitWorkspace) CurrentBranch(context.Context) (models.BranchName, error) {
	return g.branch, g.branchErr
}

func (g *fakeGitWorkspace) Head(context.Context) (models.CommitSHA, error) {
	return g.head, g.headErr
}

func (g *fakeGitWorkspace) CreateBranch(_ context.Context, name models.BranchName) error {
	if g.createErr != nil {
		return g.createErr
	}
	g.created = append(g.created, name)
	g.branch = name
	return nil
}

func (g *fakeGitWorkspace) Squash(_ context.Context, base models.CommitSHA, message string) error {
	if g.squashErr != nil {
		return g.squashErr
	}
	g.squashes = append(g.squashes, squashCall{base: base, message: message})
	g.head = base
	return nil
}

func gitRepoStore() *fakeFileStore {
	fs := newFakeFileStore()
	fs.Write(".git", "")
	return fs
}

func newIsolation(git *fakeGitWorkspace, fs *fakeFileStore) (*services.BranchIsolation, *fakeClock) {
	clock := &fakeClock{now: time.Date(2026, 7, 18, 10, 30, 0, 0, time.UTC)}
	return services.NewBranchIsolation(git, fs, clock, io.Discard), clock
}

func TestBeginCreatesTimestampedBranchOffEachDefaultBranch(t *testing.T) {
	for _, branch := range []models.BranchName{"main", "master", "develop"} {
		t.Run(string(branch), func(t *testing.T) {
			git := &fakeGitWorkspace{branch: branch, head: "abc123"}
			isolation, _ := newIsolation(git, gitRepoStore())

			state, err := isolation.Begin(context.Background())

			if err != nil {
				t.Fatalf("Begin returned error: %v", err)
			}
			want := models.BranchState{
				Created: true,
				Name:    "determined/run-20260718-103000",
				Base:    "abc123",
			}
			if state != want {
				t.Fatalf("state = %+v, want %+v", state, want)
			}
			if len(git.created) != 1 || git.created[0] != want.Name {
				t.Fatalf("created branches = %v, want exactly [%s]", git.created, want.Name)
			}
		})
	}
}

func TestBeginDoesNothingOnFeatureBranch(t *testing.T) {
	git := &fakeGitWorkspace{branch: "feat/thing", head: "abc123"}
	isolation, _ := newIsolation(git, gitRepoStore())

	state, err := isolation.Begin(context.Background())

	if err != nil {
		t.Fatalf("Begin returned error: %v", err)
	}
	if state.Created {
		t.Fatal("expected no branch creation on a feature branch")
	}
	if len(git.created) != 0 {
		t.Fatalf("created branches = %v, want none", git.created)
	}
}

func TestBeginDoesNothingOutsideGitRepository(t *testing.T) {
	git := &fakeGitWorkspace{branch: "master", head: "abc123"}
	isolation, _ := newIsolation(git, newFakeFileStore())

	state, err := isolation.Begin(context.Background())

	if err != nil {
		t.Fatalf("Begin returned error: %v", err)
	}
	if state.Created || len(git.created) != 0 {
		t.Fatalf("expected no branch creation outside a repository, got %+v", state)
	}
}

func TestBeginDoesNothingWhenBranchUnreadable(t *testing.T) {
	git := &fakeGitWorkspace{branchErr: errors.New("detached HEAD")}
	isolation, _ := newIsolation(git, gitRepoStore())

	state, err := isolation.Begin(context.Background())

	if err != nil {
		t.Fatalf("Begin returned error: %v", err)
	}
	if state.Created {
		t.Fatal("expected no branch creation when the current branch is unreadable")
	}
}

func TestBeginFailsWhenBranchCreationFails(t *testing.T) {
	git := &fakeGitWorkspace{branch: "master", head: "abc123", createErr: errors.New("boom")}
	isolation, _ := newIsolation(git, gitRepoStore())

	_, err := isolation.Begin(context.Background())

	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected creation failure to surface, got %v", err)
	}
}

func TestFinishSquashesRunCommitsWithGoalAsIntent(t *testing.T) {
	git := &fakeGitWorkspace{head: "def456"}
	fs := gitRepoStore()
	fs.Write("GOAL.md", "Add activity timers to the panel\n")
	isolation, _ := newIsolation(git, fs)
	state := models.BranchState{Created: true, Name: "determined/run-x", Base: "abc123"}

	if err := isolation.Finish(context.Background(), state); err != nil {
		t.Fatalf("Finish returned error: %v", err)
	}

	if len(git.squashes) != 1 {
		t.Fatalf("squash calls = %d, want exactly 1", len(git.squashes))
	}
	got := git.squashes[0]
	if got.base != "abc123" {
		t.Fatalf("squash base = %s, want abc123", got.base)
	}
	want := "Add activity timers to the panel\n\nSquashed from determined run commits on determined/run-x."
	if got.message != want {
		t.Fatalf("squash message = %q, want %q", got.message, want)
	}
}

func TestFinishIntentFallsBackToPlanThenGeneric(t *testing.T) {
	cases := []struct {
		name string
		seed func(*fakeFileStore)
		want string
	}{
		{
			name: "plan title when no goal",
			seed: func(fs *fakeFileStore) { fs.Write("PLAN.md", "# Ship the widget\n") },
			want: "Ship the widget",
		},
		{
			name: "generic when neither file exists",
			seed: func(*fakeFileStore) {},
			want: "determined: automated change",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			git := &fakeGitWorkspace{head: "def456"}
			fs := gitRepoStore()
			tc.seed(fs)
			isolation, _ := newIsolation(git, fs)
			state := models.BranchState{Created: true, Name: "determined/run-x", Base: "abc123"}

			if err := isolation.Finish(context.Background(), state); err != nil {
				t.Fatalf("Finish returned error: %v", err)
			}
			want := fmt.Sprintf("%s\n\nSquashed from determined run commits on determined/run-x.", tc.want)
			if git.squashes[0].message != want {
				t.Fatalf("squash message = %q, want %q", git.squashes[0].message, want)
			}
		})
	}
}

func TestFinishSkipsSquashWhenNothingCommitted(t *testing.T) {
	git := &fakeGitWorkspace{head: "abc123"}
	isolation, _ := newIsolation(git, gitRepoStore())
	state := models.BranchState{Created: true, Name: "determined/run-x", Base: "abc123"}

	if err := isolation.Finish(context.Background(), state); err != nil {
		t.Fatalf("Finish returned error: %v", err)
	}
	if len(git.squashes) != 0 {
		t.Fatalf("squash calls = %v, want none", git.squashes)
	}
}

func TestFinishSkipsRunsThatNeverBranched(t *testing.T) {
	git := &fakeGitWorkspace{head: "def456"}
	isolation, _ := newIsolation(git, gitRepoStore())

	if err := isolation.Finish(context.Background(), models.BranchState{}); err != nil {
		t.Fatalf("Finish returned error: %v", err)
	}
	if len(git.squashes) != 0 {
		t.Fatalf("squash calls = %v, want none", git.squashes)
	}
}

func TestFinishSurfacesSquashFailure(t *testing.T) {
	git := &fakeGitWorkspace{head: "def456", squashErr: errors.New("boom")}
	isolation, _ := newIsolation(git, gitRepoStore())
	state := models.BranchState{Created: true, Name: "determined/run-x", Base: "abc123"}

	err := isolation.Finish(context.Background(), state)

	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected squash failure to surface, got %v", err)
	}
}

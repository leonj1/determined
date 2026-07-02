package clients_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"determined/src/clients"
	"determined/src/models"
)

// fixedClock is a hand-written Fake clock for deterministic log filenames.
type fixedClock struct{ t time.Time }

func (c fixedClock) Now() time.Time { return c.t }

func TestUserCanReviewIterationLogAfterTheRun(t *testing.T) {
	dir := t.TempDir()
	sink := clients.NewFileLogSink(dir, fixedClock{t: time.Date(2026, 6, 20, 9, 30, 0, 0, time.UTC)})

	log, err := sink.OpenIteration(7)
	if err != nil {
		t.Fatalf("opening an iteration log should succeed: %v", err)
	}
	io.WriteString(log, "step 7 output")
	log.Close()

	path := filepath.Join(dir, "iter-0007-20260620-093000.log")
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "step 7 output") {
		t.Fatalf("expected the iteration's output persisted to %s, got %q (err %v)", path, data, err)
	}

	verifyLog, err := sink.OpenVerification(7)
	if err != nil {
		t.Fatalf("opening a verification log should succeed: %v", err)
	}
	io.WriteString(verifyLog, "verifier output")
	verifyLog.Close()

	verifyPath := filepath.Join(dir, "iter-0007-verify-20260620-093000.log")
	verifyData, err := os.ReadFile(verifyPath)
	if err != nil || !strings.Contains(string(verifyData), "verifier output") {
		t.Fatalf("expected verifier output persisted to %s, got %q (err %v)", verifyPath, verifyData, err)
	}
}

func TestStopSignalDetectsTheSentinelFile(t *testing.T) {
	dir := t.TempDir()
	stopFile := filepath.Join(dir, "STOP.md")
	signal := clients.NewOsStopSignal()

	if signal.Exists(stopFile) {
		t.Fatal("the run should not be stopped before the sentinel exists")
	}
	if err := os.WriteFile(stopFile, []byte("done"), 0o644); err != nil {
		t.Fatalf("writing the sentinel should succeed: %v", err)
	}
	if !signal.Exists(stopFile) {
		t.Fatal("the run should be considered stopped once the sentinel exists")
	}
}

func TestRunnerReportsFailureWhenToolIsMissing(t *testing.T) {
	runner := clients.NewExecCommandRunner()
	inv := models.Invocation{Binary: "definitely-not-a-real-binary-xyz", Args: []string{"exec"}}

	err := runner.Run(context.Background(), inv, io.Discard)

	if err == nil {
		t.Fatal("expected an error when the AI tool binary cannot be found")
	}
}

func TestUserCanManageProtocolFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ANSWERS.md")
	store := clients.NewOsFileStore()

	if store.Exists(path) {
		t.Fatal("protocol file should start absent")
	}
	if err := store.Write(path, "first\n"); err != nil {
		t.Fatalf("writing protocol file should succeed: %v", err)
	}
	if err := store.Append(path, "second\n"); err != nil {
		t.Fatalf("appending protocol file should succeed: %v", err)
	}
	content, err := store.Read(path)
	if err != nil || content != "first\nsecond\n" {
		t.Fatalf("expected appended protocol content, got %q (err %v)", content, err)
	}
	if !store.Exists(path) {
		t.Fatal("protocol file should exist after writing")
	}
	if err := store.Remove(path); err != nil {
		t.Fatalf("removing protocol file should succeed: %v", err)
	}
	if err := store.Remove(path); err != nil {
		t.Fatalf("removing an already-missing protocol file should succeed: %v", err)
	}
}

func TestFileStoreReportsProtocolFileErrors(t *testing.T) {
	dir := t.TempDir()
	store := clients.NewOsFileStore()

	if _, err := store.Read(filepath.Join(dir, "missing.md")); err == nil {
		t.Fatal("reading a missing protocol file should fail")
	}
	if err := store.Write(filepath.Join(dir, "missing", "PLAN.md"), "plan"); err == nil {
		t.Fatal("writing under a missing directory should fail")
	}
	if err := store.Append(dir, "answer"); err == nil {
		t.Fatal("appending to a directory should fail")
	}
}

func TestUserCanAnswerPlanningQuestionFromInput(t *testing.T) {
	var out bytes.Buffer
	prompter := clients.NewStdinPrompter(&out, strings.NewReader("  PostgreSQL  \n"))

	answer, err := prompter.Ask("Which database?")

	if err != nil || answer != "PostgreSQL" {
		t.Fatalf("expected trimmed planning answer, got %q (err %v)", answer, err)
	}
	if !strings.Contains(out.String(), "Which database?") {
		t.Fatalf("expected the question to be shown, got %q", out.String())
	}
}

func TestPrompterReportsClosedInput(t *testing.T) {
	prompter := clients.NewStdinPrompter(io.Discard, strings.NewReader(""))

	_, err := prompter.Ask("Continue?")

	if err != io.EOF {
		t.Fatalf("expected EOF when no answer is available, got %v", err)
	}
}

func TestPrompterReportsInputReadError(t *testing.T) {
	prompter := clients.NewStdinPrompter(io.Discard, failingReader{})

	_, err := prompter.Ask("Continue?")

	if err == nil {
		t.Fatal("expected the prompter to report input read errors")
	}
}

func TestUserGetsCommitAfterCompletedTaskChanges(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.email", "determined@example.test")
	runGit(t, dir, "config", "user.name", "Determined")
	if err := os.WriteFile(filepath.Join(dir, "STEPS.md"), []byte("1. [x] Add storage\n"), 0o644); err != nil {
		t.Fatalf("writing completed task file should succeed: %v", err)
	}
	restore := chdir(t, dir)
	defer restore()

	var out bytes.Buffer
	committer := clients.NewGitChangeCommitter("CHORE: save completed task changes", clients.NewExecGitRunner())

	if err := committer.Commit(context.Background(), &out); err != nil {
		t.Fatalf("committing completed task changes should succeed: %v", err)
	}
	if status := runGit(t, dir, "status", "--porcelain"); status != "" {
		t.Fatalf("expected a clean worktree after commit, got %q", status)
	}
	subject := strings.TrimSpace(runGit(t, dir, "log", "-1", "--pretty=%s"))
	if subject != "CHORE: save completed task changes" {
		t.Fatalf("expected the completed-task commit message, got %q", subject)
	}
}

func TestCleanRepoNeedsNoCompletedTaskCommit(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init")
	restore := chdir(t, dir)
	defer restore()

	var out bytes.Buffer
	committer := clients.NewGitChangeCommitter("CHORE: save completed task changes", clients.NewExecGitRunner())

	if err := committer.Commit(context.Background(), &out); err != nil {
		t.Fatalf("clean repo should not need a commit: %v", err)
	}
	if !strings.Contains(out.String(), "No repository changes to commit") {
		t.Fatalf("expected a clean-worktree message, got %q", out.String())
	}
}

func TestCommitRequiresAGitRepository(t *testing.T) {
	dir := t.TempDir()
	restore := chdir(t, dir)
	defer restore()

	committer := clients.NewGitChangeCommitter("CHORE: save completed task changes", clients.NewExecGitRunner())

	if err := committer.Commit(context.Background(), io.Discard); err == nil {
		t.Fatal("committing should fail outside a Git repository")
	}
}

func TestCommitReportsCleanRepoOutputError(t *testing.T) {
	committer := clients.NewGitChangeCommitter("CHORE: save completed task changes", fakeGitRunner{})

	if err := committer.Commit(context.Background(), failingWriter{}); err == nil {
		t.Fatal("clean repo commit should report output write failures")
	}
}

func TestSystemClockAdvances(t *testing.T) {
	clock := clients.NewSystemClock()
	before := clock.Now()
	time.Sleep(time.Millisecond)
	if !clock.Now().After(before) {
		t.Fatal("the system clock should move forward")
	}
}

func chdir(t *testing.T, dir string) func() {
	t.Helper()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatalf("reading current directory should succeed: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("changing to temp repo should succeed: %v", err)
	}
	return func() {
		if err := os.Chdir(previous); err != nil {
			t.Fatalf("restoring current directory should succeed: %v", err)
		}
	}
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v should succeed: %s: %v", args, out, err)
	}
	return string(out)
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) {
	return 0, os.ErrPermission
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, os.ErrPermission
}

type fakeGitRunner struct{}

func (fakeGitRunner) Run(context.Context, io.Writer, io.Writer, string, ...string) error {
	return nil
}

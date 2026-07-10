package clients_test

import (
	"bytes"
	"context"
	"io"
	"os"
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

func TestRunnerReportsFailureWhenToolIsMissing(t *testing.T) {
	runner := clients.NewExecCommandRunner()
	inv := models.Invocation{Binary: "definitely-not-a-real-binary-xyz", Args: []string{"exec"}}

	err := runner.Run(context.Background(), inv, io.Discard)

	if err == nil {
		t.Fatal("expected an error when the AI tool binary cannot be found")
	}
}

func TestRunnerAppendsInvocationEnvToTheInheritedEnvironment(t *testing.T) {
	t.Setenv("DET_TEST_INHERITED", "kept")
	runner := clients.NewExecCommandRunner()
	var out bytes.Buffer
	inv := models.Invocation{
		Binary: "sh",
		Args:   []string{"-c", "echo $DET_OUTCOME $DET_TEST_INHERITED"},
		Env:    []string{"DET_OUTCOME=success"},
	}

	err := runner.Run(context.Background(), inv, &out)

	if err != nil || strings.TrimSpace(out.String()) != "success kept" {
		t.Fatalf("expected the child to see the injected and inherited env, got %q (err %v)", out.String(), err)
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

func TestSystemClockAdvances(t *testing.T) {
	clock := clients.NewSystemClock()
	before := clock.Now()
	time.Sleep(time.Millisecond)
	if !clock.Now().After(before) {
		t.Fatal("the system clock should move forward")
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) {
	return 0, os.ErrPermission
}

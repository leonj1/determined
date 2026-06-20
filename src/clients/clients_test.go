package clients_test

import (
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

func TestSystemClockAdvances(t *testing.T) {
	clock := clients.NewSystemClock()
	before := clock.Now()
	time.Sleep(time.Millisecond)
	if !clock.Now().After(before) {
		t.Fatal("the system clock should move forward")
	}
}

package services_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"determined/src/models"
	"determined/src/services"
)

const planContent = "# Plan\nBuild the widget.\n"

// tamperConfig protects PLAN.md, TESTS.md, and CRITERIA.md like a real run.
func tamperConfig() models.Config {
	c := config(0)
	c.ProtectedFiles = []string{"PLAN.md", "TESTS.md", "CRITERIA.md"}
	return c
}

func TestTamperedPlanIsRestoredAndRecorded(t *testing.T) {
	fs := stepsFileStore()
	fs.Write("PLAN.md", planContent)
	var terminal bytes.Buffer
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		switch call {
		case 1: // worker cheats: weakens the plan, then claims both steps done
			fs.Write("PLAN.md", "# Plan\nDo nothing.\n")
			fs.Write("STEPS.md", twoStepsAllChecked)
		case 2: // audit approves
			fs.Write("STOP.md", "audit: plan satisfied")
		}
		return nil
	}}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, &terminal, tamperConfig())
	o.Run(context.Background())

	if got, _ := fs.Read("PLAN.md"); got != planContent {
		t.Fatalf("PLAN.md not restored: %q", got)
	}
	fixes, err := fs.Read("FIXES.md")
	if err != nil {
		t.Fatalf("no FIXES.md note recorded: %v", err)
	}
	if !strings.Contains(fixes, "Iteration 1 modified PLAN.md") {
		t.Fatalf("FIXES.md missing tamper note: %q", fixes)
	}
	if !strings.Contains(terminal.String(), "tool modified protected file PLAN.md during iteration 1") {
		t.Fatalf("terminal missing tamper warning: %q", terminal.String())
	}
}

func TestDeletedProtectedFileIsRestored(t *testing.T) {
	fs := stepsFileStore()
	fs.Write("PLAN.md", planContent)
	fs.Write("TESTS.md", "## Tests\n- widget renders\n")
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		switch call {
		case 1:
			fs.Remove("TESTS.md")
			fs.Write("STEPS.md", twoStepsAllChecked)
		case 2:
			fs.Write("STOP.md", "audit: plan satisfied")
		}
		return nil
	}}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, tamperConfig())
	o.Run(context.Background())

	if got, _ := fs.Read("TESTS.md"); got != "## Tests\n- widget renders\n" {
		t.Fatalf("TESTS.md not restored: %q", got)
	}
}

func TestProtectedFileCreatedByToolIsRemoved(t *testing.T) {
	fs := stepsFileStore() // no CRITERIA.md before the run
	fs.Write("PLAN.md", planContent)
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		switch call {
		case 1: // worker plants trivially-passing criteria
			fs.Write("CRITERIA.md", "Feature: nothing\n")
			fs.Write("STEPS.md", twoStepsAllChecked)
		case 2:
			fs.Write("STOP.md", "audit: plan satisfied")
		}
		return nil
	}}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, tamperConfig())
	o.Run(context.Background())

	if fs.Exists("CRITERIA.md") {
		content, _ := fs.Read("CRITERIA.md")
		t.Fatalf("tool-created CRITERIA.md not removed: %q", content)
	}
}

func TestLegitimateStepsProgressTriggersNoTamperWarning(t *testing.T) {
	fs := stepsFileStore()
	fs.Write("PLAN.md", planContent)
	var terminal bytes.Buffer
	runner := &fakeRunner{script: func(call int, _ io.Writer) error {
		switch call {
		case 1:
			fs.Write("STEPS.md", twoStepsFirstChecked)
		case 2:
			fs.Write("STEPS.md", twoStepsAllChecked)
		case 3:
			fs.Write("STOP.md", "audit: plan satisfied")
		}
		return nil
	}}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, &terminal, tamperConfig())
	o.Run(context.Background())

	if fs.Exists("FIXES.md") {
		fixes, _ := fs.Read("FIXES.md")
		t.Fatalf("unexpected tamper note for legitimate work: %q", fixes)
	}
	if strings.Contains(terminal.String(), "protected file") {
		t.Fatalf("unexpected tamper warning: %q", terminal.String())
	}
}

func TestTamperOnFailedInvocationIsStillReverted(t *testing.T) {
	fs := stepsFileStore()
	fs.Write("PLAN.md", planContent)
	cfg := tamperConfig()
	cfg.MaxConsecutiveFailures = 1
	runner := &fakeRunner{script: func(int, io.Writer) error {
		fs.Write("PLAN.md", "# Plan\nDo nothing.\n") // tamper, then crash
		return errors.New("tool crashed")
	}}
	o := services.NewOrchestrator(runner, fs, &fakeClock{now: time.Now()}, &fakeLogSink{}, io.Discard, cfg)
	o.Run(context.Background())

	if got, _ := fs.Read("PLAN.md"); got != planContent {
		t.Fatalf("PLAN.md not restored after failed invocation: %q", got)
	}
}

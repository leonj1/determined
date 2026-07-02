package services_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"testing"

	"determined/src/models"
	"determined/src/services"
)

type fakeTool struct{}

func (fakeTool) Name() string { return "fake" }

func (fakeTool) Invocation(prompt string) models.Invocation {
	return models.Invocation{Binary: "fake", Args: []string{prompt}}
}

func TestVerifierApprovesChangesWhenToolReturnsPass(t *testing.T) {
	runner := &fakeRunner{script: func(_ int, out io.Writer) error {
		fmt.Fprintln(out, "PASS: changes match the step")
		return nil
	}}
	verifier := services.NewToolChangeVerifier(runner, fakeTool{})

	result, err := verifier.Verify(context.Background(), models.Step{Number: 2, Text: "Add storage"}, &bytes.Buffer{})

	if err != nil || !result.Passed() {
		t.Fatalf("expected verifier approval, got %#v (err %v)", result, err)
	}
	prompt := runner.invocations[0].Args[0]
	if !strings.Contains(prompt, "Step 2: Add storage") || !strings.Contains(prompt, "Do not modify files") {
		t.Fatalf("expected verifier prompt to name the intended step and stay read-only, got %q", prompt)
	}
}

func TestVerifierReturnsRepairFeedbackWhenToolReturnsFail(t *testing.T) {
	runner := &fakeRunner{script: func(_ int, out io.Writer) error {
		fmt.Fprintln(out, "FAIL: missing AGENTS.md convention check")
		return nil
	}}
	verifier := services.NewToolChangeVerifier(runner, fakeTool{})

	result, err := verifier.Verify(context.Background(), models.Step{Number: 1, Text: "Wire CLI"}, &bytes.Buffer{})

	if err != nil || result.Passed() {
		t.Fatalf("expected verifier rejection, got %#v (err %v)", result, err)
	}
	if !strings.Contains(result.Feedback, "missing AGENTS.md") {
		t.Fatalf("expected verifier feedback to be preserved, got %q", result.Feedback)
	}
}

func TestVerifierRejectsAmbiguousToolOutput(t *testing.T) {
	runner := &fakeRunner{script: func(_ int, out io.Writer) error {
		fmt.Fprintln(out, "looks fine to me")
		return nil
	}}
	verifier := services.NewToolChangeVerifier(runner, fakeTool{})

	result, err := verifier.Verify(context.Background(), models.Step{Number: 1, Text: "Wire CLI"}, &bytes.Buffer{})

	if err != nil || result.Passed() {
		t.Fatalf("expected ambiguous verifier output to reject, got %#v (err %v)", result, err)
	}
	if !strings.Contains(result.Feedback, "did not return PASS") {
		t.Fatalf("expected ambiguous-output feedback, got %q", result.Feedback)
	}
}

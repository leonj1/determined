package main

import (
	"flag"
	"io"
	"testing"
	"time"

	"determined/src/models"
)

func TestUserCanRunUpdateCommand(t *testing.T) {
	if !isUpdateCommand([]string{"determined", "update"}) {
		t.Fatal("update subcommand should be recognized")
	}
}

func TestNormalRunIsNotUpdateCommand(t *testing.T) {
	if isUpdateCommand([]string{"determined", "--version"}) {
		t.Fatal("normal flags should not be treated as update")
	}
}

func TestUserCanSelectMVPPlanning(t *testing.T) {
	mode, err := selectPlanMode(true, false, true, false)
	if err != nil || mode != models.PlanModeMVP {
		t.Fatalf("expected MVP planning, got mode %q and error %v", mode, err)
	}
}

func TestUserCanSelectPrototypePlanning(t *testing.T) {
	mode, err := selectPlanMode(true, false, false, true)
	if err != nil || mode != models.PlanModePrototype {
		t.Fatalf("expected prototype planning, got mode %q and error %v", mode, err)
	}
}

func TestUserCannotCombinePlanningModes(t *testing.T) {
	if _, err := selectPlanMode(true, false, true, true); err == nil {
		t.Fatal("expected combined MVP and prototype modes to be rejected")
	}
}

func TestUserCannotSelectPlanningModeDuringExecution(t *testing.T) {
	if _, err := selectPlanMode(false, false, true, false); err == nil {
		t.Fatal("expected MVP without -plan to be rejected")
	}
}

func TestUserCannotCreateAndReviewPlanTogether(t *testing.T) {
	if _, err := selectPlanMode(true, true, false, false); err == nil {
		t.Fatal("expected -plan and -review-plan together to be rejected")
	}
}

func TestUserCannotApplyCreationModesToReview(t *testing.T) {
	if _, err := selectPlanMode(false, true, true, false); err == nil {
		t.Fatal("expected -mvp with -review-plan to be rejected")
	}
}

func TestPrototypeSkipsQualityRefinement(t *testing.T) {
	if got := refinePasses(models.PlanModePrototype, 5); got != 0 {
		t.Fatalf("prototype refinement passes = %d, want 0", got)
	}
}

func TestMVPStillUsesConfiguredQualityRefinement(t *testing.T) {
	if got := refinePasses(models.PlanModeMVP, 3); got != 3 {
		t.Fatalf("MVP refinement passes = %d, want 3", got)
	}
}

func TestUserCanSetMaxDurationWithShortFlag(t *testing.T) {
	flags := flag.NewFlagSet("determined", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	budget := registerBudgetFlags(flags)

	if err := flags.Parse([]string{"-t", "2h"}); err != nil {
		t.Fatalf("short max-duration flag should parse: %v", err)
	}
	if *budget != 2*time.Hour {
		t.Fatalf("short max-duration flag set %v, want 2h", *budget)
	}
}

func TestUserCanSetMaxDurationWithLongFlag(t *testing.T) {
	flags := flag.NewFlagSet("determined", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	budget := registerBudgetFlags(flags)

	if err := flags.Parse([]string{"--max-duration", "3h"}); err != nil {
		t.Fatalf("long max-duration flag should parse: %v", err)
	}
	if *budget != 3*time.Hour {
		t.Fatalf("long max-duration flag set %v, want 3h", *budget)
	}
}

func TestUserCanSelectInitialization(t *testing.T) {
	flags := flag.NewFlagSet("determined", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	initialize := registerInitFlag(flags)

	if err := flags.Parse([]string{"-init"}); err != nil {
		t.Fatalf("init flag should parse: %v", err)
	}
	if !*initialize {
		t.Fatal("-init should select initialization")
	}
}

func TestInitializationUsesPersonalKnowledgeDestinations(t *testing.T) {
	cfg := initializationConfig("/home/jose")

	if len(cfg.Documents) != 2 {
		t.Fatalf("initialization document count = %d, want 2", len(cfg.Documents))
	}
	if cfg.Documents[0].Destination != "/home/jose/.claude/CLAUDE.md" {
		t.Fatalf("unexpected Claude destination %q", cfg.Documents[0].Destination)
	}
	if cfg.Documents[1].Destination != "/home/jose/AGENTS.md" {
		t.Fatalf("unexpected Agents destination %q", cfg.Documents[1].Destination)
	}
	if cfg.Documents[0].Source != "https://raw.githubusercontent.com/leonj1/open-doc-format/master/personal-knowledge/CLAUDE.md" {
		t.Fatalf("unexpected Claude source %q", cfg.Documents[0].Source)
	}
	if cfg.Documents[1].Source != "https://raw.githubusercontent.com/leonj1/open-doc-format/master/personal-knowledge/AGENTS.md" {
		t.Fatalf("unexpected Agents source %q", cfg.Documents[1].Source)
	}
}

func TestPlanInputPreservesTrailingWordsSplitByShell(t *testing.T) {
	got := planInput("#", []string{"Goal", "Build", "the", "TODO", "CLI"})
	want := "# Goal Build the TODO CLI"

	if got != want {
		t.Fatalf("plan input = %q, want %q", got, want)
	}
}

func TestPlanInputKeepsQuotedValueWhenNoTrailingWords(t *testing.T) {
	got := planInput("build a todo CLI", nil)

	if got != "build a todo CLI" {
		t.Fatalf("plan input = %q, want the quoted value unchanged", got)
	}
}

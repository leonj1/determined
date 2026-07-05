package main

import (
	"flag"
	"io"
	"testing"
	"time"
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

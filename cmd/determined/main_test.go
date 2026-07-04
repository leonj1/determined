package main

import "testing"

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

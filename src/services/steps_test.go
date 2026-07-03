package services_test

import (
	"reflect"
	"testing"

	"determined/src/services"
)

func TestParseSteps(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []services.Step
	}{
		{
			name: "completed and incomplete steps with done-when lines",
			in: "# STEPS\n\n" +
				"- [x] 1. Ship the parser.\n" +
				"  Done when: `go test ./...` passes.\n\n" +
				"- [ ] 2. Wire it into the loop.\n" +
				"  Done when: each iteration embeds the next step.\n",
			want: []services.Step{
				{Text: "1. Ship the parser.", DoneWhen: "`go test ./...` passes.", Completed: true},
				{Text: "2. Wire it into the loop.", DoneWhen: "each iteration embeds the next step.", Completed: false},
			},
		},
		{
			name: "uppercase X counts as completed",
			in:   "- [X] Done already.",
			want: []services.Step{{Text: "Done already.", Completed: true}},
		},
		{
			name: "step without a done-when line",
			in:   "- [ ] Just do it.",
			want: []services.Step{{Text: "Just do it.", Completed: false}},
		},
		{
			name: "indented continuation lines fold into the text",
			in: "- [ ] A step whose description\n" +
				"  spills onto a second line.\n" +
				"  Done when: it parses.\n",
			want: []services.Step{{
				Text:     "A step whose description spills onto a second line.",
				DoneWhen: "it parses.",
			}},
		},
		{
			name: "done-when prefix matches case-insensitively",
			in:   "- [ ] Step.\n  done when: tests pass.",
			want: []services.Step{{Text: "Step.", DoneWhen: "tests pass."}},
		},
		{
			name: "star bullets are accepted",
			in:   "* [ ] Star step.\n  Done when: parsed.",
			want: []services.Step{{Text: "Star step.", DoneWhen: "parsed."}},
		},
		{
			name: "malformed lines are ignored",
			in: "# Heading\n\nSome prose.\n\n" +
				"- plain bullet without a checkbox\n" +
				"- [y] unknown mark\n" +
				"- []no space in the box\n" +
				"- [ ] The one real step.\n",
			want: []services.Step{{Text: "The one real step.", Completed: false}},
		},
		{
			name: "done-when after a blank line belongs to no step",
			in:   "- [ ] Step.\n\nDone when: orphaned criterion.\n",
			want: []services.Step{{Text: "Step.", Completed: false}},
		},
		{
			name: "unindented prose ends the item",
			in:   "- [ ] Step.\nThis paragraph is not part of the step.\n  Done when: never attached.\n",
			want: []services.Step{{Text: "Step.", Completed: false}},
		},
		{
			name: "empty input",
			in:   "",
			want: nil,
		},
		{
			name: "no checkbox items at all",
			in:   "Just a plain file\nwith prose and\n1. an ordered list.",
			want: nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := services.ParseSteps(c.in)
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("ParseSteps(%q) = %#v, want %#v", c.in, got, c.want)
			}
		})
	}
}

func TestNextIncompleteStep(t *testing.T) {
	cases := []struct {
		name     string
		steps    []services.Step
		want     services.Step
		wantFind bool
	}{
		{
			name: "first unchecked step wins",
			steps: []services.Step{
				{Text: "one", Completed: true},
				{Text: "two", DoneWhen: "tests pass"},
				{Text: "three"},
			},
			want:     services.Step{Text: "two", DoneWhen: "tests pass"},
			wantFind: true,
		},
		{
			name:     "all complete finds nothing",
			steps:    []services.Step{{Text: "one", Completed: true}},
			wantFind: false,
		},
		{
			name:     "empty list finds nothing",
			steps:    nil,
			wantFind: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := services.NextIncompleteStep(c.steps)
			if ok != c.wantFind || !reflect.DeepEqual(got, c.want) {
				t.Fatalf("NextIncompleteStep(%#v) = %#v, %v; want %#v, %v",
					c.steps, got, ok, c.want, c.wantFind)
			}
		})
	}
}

func TestAllStepsComplete(t *testing.T) {
	cases := []struct {
		name  string
		steps []services.Step
		want  bool
	}{
		{
			name:  "all checked",
			steps: []services.Step{{Text: "one", Completed: true}, {Text: "two", Completed: true}},
			want:  true,
		},
		{
			name:  "one unchecked",
			steps: []services.Step{{Text: "one", Completed: true}, {Text: "two"}},
			want:  false,
		},
		{
			name:  "empty list is not complete",
			steps: nil,
			want:  false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := services.AllStepsComplete(c.steps); got != c.want {
				t.Fatalf("AllStepsComplete(%#v) = %v, want %v", c.steps, got, c.want)
			}
		})
	}
}

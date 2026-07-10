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

func TestUncheckSteps(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		indices []int
		want    string
	}{
		{
			name: "flips only the targeted box, preserving everything else byte for byte",
			in: "# STEPS\n\n" +
				"- [x] 1. Ship the parser.\n" +
				"  Done when: `go test ./...` passes.\n\n" +
				"- [x] 2. Wire it into the loop.\n" +
				"  Done when: each iteration embeds the next step.\n",
			indices: []int{1},
			want: "# STEPS\n\n" +
				"- [x] 1. Ship the parser.\n" +
				"  Done when: `go test ./...` passes.\n\n" +
				"- [ ] 2. Wire it into the loop.\n" +
				"  Done when: each iteration embeds the next step.\n",
		},
		{
			name:    "multiple indices flip together",
			in:      "- [x] One.\n- [x] Two.\n- [x] Three.\n",
			indices: []int{0, 2},
			want:    "- [ ] One.\n- [x] Two.\n- [ ] Three.\n",
		},
		{
			name:    "uppercase X marks are flipped too",
			in:      "- [X] Done already.\n",
			indices: []int{0},
			want:    "- [ ] Done already.\n",
		},
		{
			name:    "indentation and star bullets survive",
			in:      "  * [x] Star step.\n    Done when: parsed.\n",
			indices: []int{0},
			want:    "  * [ ] Star step.\n    Done when: parsed.\n",
		},
		{
			name:    "an already unchecked target is left as is",
			in:      "- [ ] Not done.\n",
			indices: []int{0},
			want:    "- [ ] Not done.\n",
		},
		{
			name: "non-checkbox lines are neither counted nor touched",
			in: "- [x] Real one.\n" +
				"- a plain bullet mentioning [x]\n" +
				"prose mentioning [x] too\n" +
				"- [x] Real two.\n",
			indices: []int{1},
			want: "- [x] Real one.\n" +
				"- a plain bullet mentioning [x]\n" +
				"prose mentioning [x] too\n" +
				"- [ ] Real two.\n",
		},
		{
			name:    "out-of-range indices are ignored",
			in:      "- [x] Only step.\n",
			indices: []int{3},
			want:    "- [x] Only step.\n",
		},
		{
			name:    "no indices changes nothing",
			in:      "- [x] Only step.\n",
			indices: nil,
			want:    "- [x] Only step.\n",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := services.UncheckSteps(c.in, c.indices); got != c.want {
				t.Fatalf("UncheckSteps(%q, %v) = %q, want %q", c.in, c.indices, got, c.want)
			}
		})
	}
}

func TestCheckSteps(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		indices []int
		want    string
	}{
		{
			name: "flips only the targeted box, preserving everything else byte for byte",
			in: "# STEPS\n\n" +
				"- [ ] 1. Ship the parser.\n" +
				"  Done when: `go test ./...` passes.\n\n" +
				"- [ ] 2. Wire it into the loop.\n" +
				"  Done when: each iteration embeds the next step.\n",
			indices: []int{1},
			want: "# STEPS\n\n" +
				"- [ ] 1. Ship the parser.\n" +
				"  Done when: `go test ./...` passes.\n\n" +
				"- [x] 2. Wire it into the loop.\n" +
				"  Done when: each iteration embeds the next step.\n",
		},
		{
			name:    "multiple indices flip together",
			in:      "- [ ] One.\n- [ ] Two.\n- [ ] Three.\n",
			indices: []int{0, 2},
			want:    "- [x] One.\n- [ ] Two.\n- [x] Three.\n",
		},
		{
			name:    "indentation and star bullets survive",
			in:      "  * [ ] Star step.\n    Done when: parsed.\n",
			indices: []int{0},
			want:    "  * [x] Star step.\n    Done when: parsed.\n",
		},
		{
			name:    "an already checked target is left as is, even an uppercase X",
			in:      "- [X] Done already.\n",
			indices: []int{0},
			want:    "- [X] Done already.\n",
		},
		{
			name: "non-checkbox lines are neither counted nor touched",
			in: "- [ ] Real one.\n" +
				"- a plain bullet mentioning [ ]\n" +
				"prose mentioning [ ] too\n" +
				"- [ ] Real two.\n",
			indices: []int{1},
			want: "- [ ] Real one.\n" +
				"- a plain bullet mentioning [ ]\n" +
				"prose mentioning [ ] too\n" +
				"- [x] Real two.\n",
		},
		{
			name:    "out-of-range indices are ignored",
			in:      "- [ ] Only step.\n",
			indices: []int{3},
			want:    "- [ ] Only step.\n",
		},
		{
			name:    "no indices changes nothing",
			in:      "- [ ] Only step.\n",
			indices: nil,
			want:    "- [ ] Only step.\n",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := services.CheckSteps(c.in, c.indices); got != c.want {
				t.Fatalf("CheckSteps(%q, %v) = %q, want %q", c.in, c.indices, got, c.want)
			}
		})
	}
}

func TestCheckAndUncheckStepsRoundTripByteExactly(t *testing.T) {
	original := "# STEPS\n\n" +
		"- [x] 1. Ship the parser.\n" +
		"  Done when: `go test ./...` passes.\n\n" +
		"  * [ ] 2. Star step.\n    Done when: parsed.\n\n" +
		"prose mentioning [ ] and [x]\n" +
		"- [ ] 3. Wire it into the loop.\n" +
		"  Done when: each iteration embeds the next step.\n"
	indices := []int{1, 2}

	checked := services.CheckSteps(original, indices)
	if got := services.UncheckSteps(checked, indices); got != original {
		t.Fatalf("UncheckSteps(CheckSteps(...)) = %q, want the original %q", got, original)
	}
	unchecked := services.UncheckSteps(original, []int{0})
	if got := services.CheckSteps(unchecked, []int{0}); got != original {
		t.Fatalf("CheckSteps(UncheckSteps(...)) = %q, want the original %q", got, original)
	}
}

func TestNextIncompleteStep(t *testing.T) {
	cases := []struct {
		name     string
		steps    []services.Step
		wantIdx  int
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
			wantIdx:  1,
			want:     services.Step{Text: "two", DoneWhen: "tests pass"},
			wantFind: true,
		},
		{
			name:     "all complete finds nothing",
			steps:    []services.Step{{Text: "one", Completed: true}},
			wantIdx:  -1,
			wantFind: false,
		},
		{
			name:     "empty list finds nothing",
			steps:    nil,
			wantIdx:  -1,
			wantFind: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			idx, got, ok := services.NextIncompleteStep(c.steps)
			if idx != c.wantIdx || ok != c.wantFind || !reflect.DeepEqual(got, c.want) {
				t.Fatalf("NextIncompleteStep(%#v) = %d, %#v, %v; want %d, %#v, %v",
					c.steps, idx, got, ok, c.wantIdx, c.want, c.wantFind)
			}
		})
	}
}

func TestCompletedStepCount(t *testing.T) {
	cases := []struct {
		name  string
		steps []services.Step
		want  int
	}{
		{
			name:  "counts only checked steps",
			steps: []services.Step{{Text: "one", Completed: true}, {Text: "two"}, {Text: "three", Completed: true}},
			want:  2,
		},
		{
			name:  "none checked",
			steps: []services.Step{{Text: "one"}, {Text: "two"}},
			want:  0,
		},
		{
			name:  "empty list",
			steps: nil,
			want:  0,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := services.CompletedStepCount(c.steps); got != c.want {
				t.Fatalf("CompletedStepCount(%#v) = %d, want %d", c.steps, got, c.want)
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

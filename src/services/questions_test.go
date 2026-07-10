package services_test

import (
	"reflect"
	"testing"

	"determined/src/services"
)

func TestParseQuestionsHandlesListStyles(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "numbered with dot",
			in:   "1. What database?\n2. Auth required?\n",
			want: []string{"What database?", "Auth required?"},
		},
		{
			name: "numbered with paren",
			in:   "1) First?\n2) Second?",
			want: []string{"First?", "Second?"},
		},
		{
			name: "bulleted",
			in:   "- Dash one?\n* Star two?",
			want: []string{"Dash one?", "Star two?"},
		},
		{
			name: "ignores prose and headings around the list",
			in:   "# Questions\n\nHere are my questions:\n\n1. Real question?\n\nThanks!",
			want: []string{"Real question?"},
		},
		{
			name: "indented list items",
			in:   "  1. Indented?\n    - Also indented?",
			want: []string{"Indented?", "Also indented?"},
		},
		{
			name: "no list items",
			in:   "I have no questions, the plan is clear.",
			want: nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := services.ParseQuestions(c.in)
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("ParseQuestions(%q) = %#v, want %#v", c.in, got, c.want)
			}
		})
	}
}

func TestRefinementIssuesTreatsNoneAsDone(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"bare none", "NONE", nil},
		{"lowercase none in list", "- none", nil},
		{"numbered none", "1. None", nil},
		{"prose with no list", "All steps look good to me.", nil},
		{"empty", "", nil},
		{"real oversized steps", "1. Build the whole backend\n2. Build the whole frontend", []string{"Build the whole backend", "Build the whole frontend"}},
		{"none alongside a real step stays flagged", "- NONE\n- Build everything", []string{"NONE", "Build everything"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := services.RefinementIssues(c.in)
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("RefinementIssues(%q) = %#v, want %#v", c.in, got, c.want)
			}
		})
	}
}

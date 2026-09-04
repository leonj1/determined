package models

import "strings"

type PromptID uint64

type PromptKind string

const (
	PromptKindText    PromptKind = "text"
	PromptKindConfirm PromptKind = "confirm"
	PromptKindChoice  PromptKind = "choice"
)

type PromptChoice struct {
	Value       string `json:"value"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

type UserPrompt struct {
	ID         PromptID       `json:"id"`
	Title      string         `json:"title"`
	Body       string         `json:"body"`
	Kind       PromptKind     `json:"kind"`
	Choices    []PromptChoice `json:"choices,omitempty"`
	AllowEmpty bool           `json:"allowEmpty"`
}

func TextPrompt(title, body string, allowEmpty bool) UserPrompt {
	return UserPrompt{Title: title, Body: body, Kind: PromptKindText, AllowEmpty: allowEmpty}
}

func ConfirmPrompt(title, body string, allowEmpty bool) UserPrompt {
	return UserPrompt{Title: title, Body: body, Kind: PromptKindConfirm, AllowEmpty: allowEmpty}
}

func ChoicePrompt(title, body string, choices []PromptChoice) UserPrompt {
	return UserPrompt{Title: title, Body: body, Kind: PromptKindChoice, Choices: choices}
}

func (p UserPrompt) Accepts(answer string) bool {
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return p.AllowEmpty
	}
	if p.Kind != PromptKindChoice {
		return true
	}
	for _, choice := range p.Choices {
		if answer == choice.Value {
			return true
		}
	}
	return false
}

type PromptResponse struct {
	ID     PromptID `json:"id"`
	Answer string   `json:"answer"`
}

type PromptSubmissionResult string

const (
	PromptSubmissionAccepted PromptSubmissionResult = "accepted"
	PromptSubmissionMissing  PromptSubmissionResult = "missing"
	PromptSubmissionStale    PromptSubmissionResult = "stale"
	PromptSubmissionInvalid  PromptSubmissionResult = "invalid"
)

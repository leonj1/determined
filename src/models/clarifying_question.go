package models

// ClarifyingQuestion is a planning question with optional finite answers.
// Questions with choices can be presented as buttons; questions without them
// remain open-ended.
type ClarifyingQuestion struct {
	Body    string
	Choices []PromptChoice
}

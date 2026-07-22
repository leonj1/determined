package models

// TaskAction is a user command from the status page aimed at the task the
// session is currently working on.
type TaskAction int

const (
	// TaskActionNone means no command is pending.
	TaskActionNone TaskAction = iota
	// TaskActionSkip means abort the active task and let the run move on.
	TaskActionSkip
	// TaskActionStop means abort the active task and end the whole run.
	TaskActionStop
)

package services

import (
	"context"
	"fmt"
	"io"

	"determined/src/models"
)

// ExplanationService generates the post-execution explanation and quiz on
// demand, used both after successful interactive runs and when the user
// requests one from the status page after a halted run.
type ExplanationService struct {
	runner   CommandRunner
	files    FileStore
	clock    Clock
	logs     LogSink
	terminal io.Writer
	status   ExecStatusReporter
	tool     models.Tool
	cfg      models.Config

	iteration int
}

// NewExplanationService wires an ExplanationService from its dependencies.
func NewExplanationService(
	runner CommandRunner,
	files FileStore,
	clock Clock,
	logs LogSink,
	terminal io.Writer,
	status ExecStatusReporter,
	tool models.Tool,
	cfg models.Config,
) *ExplanationService {
	return &ExplanationService{
		runner:   runner,
		files:    files,
		clock:    clock,
		logs:     logs,
		terminal: terminal,
		status:   status,
		tool:     tool,
		cfg:      cfg,
	}
}

// Run generates the explanation and, on success, the quiz. It reports whether
// the explanation was produced.
func (e *ExplanationService) Run(ctx context.Context) bool {
	e.status.StartExplanation()
	result := e.invoke(ctx, explainPrompt(e.cfg), "explaining the changes")
	if !result {
		e.status.FinishExplanation(false)
		return false
	}
	content, err := e.files.Read(e.cfg.ExplanationFile)
	if err != nil {
		e.status.FinishExplanation(false)
		return false
	}
	e.status.SetExplanation(content)
	e.status.FinishExplanation(true)
	e.quizRun(ctx, content)
	return true
}

// quizRun generates and validates the quiz from the explanation.
func (e *ExplanationService) quizRun(ctx context.Context, explanation string) {
	e.status.StartQuiz()
	prompt := quizPrompt(e.cfg)
	content, ok := e.quizInvoke(ctx, prompt)
	if !ok {
		e.status.FinishQuiz(false)
		return
	}
	questions, err := ParseQuiz(content, explanation)
	if err != nil {
		prompt = quizRetryPrompt(e.cfg, err)
		content, ok = e.quizInvoke(ctx, prompt)
		if ok {
			questions, err = ParseQuiz(content, explanation)
		}
	}
	if !ok || err != nil {
		e.status.FinishQuiz(false)
		return
	}
	e.status.SetQuiz(questions)
	e.status.FinishQuiz(true)
}

// invoke runs one tool invocation, teeing output to the terminal and a log.
func (e *ExplanationService) invoke(
	ctx context.Context,
	prompt string,
	progress progressMessage,
) bool {
	e.iteration++
	log, err := e.logs.OpenIteration(e.iteration)
	if err != nil {
		return false
	}
	defer log.Close()
	out := io.MultiWriter(e.terminal, log)
	writeProgress(out, e.clock, progress)
	notifyProgress(e.status, progress)
	entry := -1
	if e.status != nil {
		entry = e.status.BeginExecLogEntry(string(progress))
		statusLog := newLogEntryWriter(execOutputSink{e.status})
		defer statusLog.Flush()
		out = io.MultiWriter(out, statusLog)
	}
	err = e.runner.Run(ctx, e.tool.Invocation(prompt), out)
	if e.status != nil && entry >= 0 {
		if err != nil {
			e.status.SettleExecLogEntryAt(entry, models.EntryStateError)
		} else {
			e.status.SettleExecLogEntryAt(entry, models.EntryStateOK)
		}
	}
	if err != nil {
		fmt.Fprintf(e.terminal,
			"determined: explanation invocation failed: %v\n", err)
		return false
	}
	return true
}

// quizInvoke runs a quiz-generation invocation and returns the file content.
func (e *ExplanationService) quizInvoke(ctx context.Context, prompt string) (string, bool) {
	if !e.invoke(ctx, prompt, "writing the quiz") {
		return "", false
	}
	content, err := e.files.Read(e.cfg.QuizFile)
	return content, err == nil
}

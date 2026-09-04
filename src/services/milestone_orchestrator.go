package services

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"determined/src/models"
)

// MilestoneOrchestrator runs bounded milestone gates around the step orchestrator.
type MilestoneOrchestrator struct {
	runner   CommandRunner
	files    FileStore
	clock    Clock
	logs     LogSink
	terminal io.Writer
	cfg      models.Config
	newInner func(models.Config) *Orchestrator
	status   ExecStatusReporter
}

// NewMilestoneOrchestrator injects every outer-loop dependency before execution.
func NewMilestoneOrchestrator(r CommandRunner, f FileStore, c Clock, l LogSink, t io.Writer, cfg models.Config, n func(models.Config) *Orchestrator, status ExecStatusReporter) *MilestoneOrchestrator {
	return &MilestoneOrchestrator{runner: r, files: f, clock: c, logs: l, terminal: t, cfg: cfg, newInner: n, status: status}
}

// innerConfig protects the milestone document only inside worker execution.
func innerConfig(cfg models.Config) models.Config {
	out := cfg
	found := false
	for _, f := range out.ProtectedFiles {
		found = found || f == cfg.MilestonesFile
	}
	if !found {
		out.ProtectedFiles = append(append([]string{}, out.ProtectedFiles...), cfg.MilestonesFile)
	}
	return out
}

// InnerConfig exposes the pure inner-loop configuration for integration tests.
func InnerConfig(cfg models.Config) models.Config { return innerConfig(cfg) }

// Run processes milestones in order until all verify or a bounded guard stops.
func (o *MilestoneOrchestrator) Run(ctx context.Context) models.Outcome {
	doc, state, outcome, ok := o.load()
	if !ok {
		return outcome
	}
	for !AllVerified(state) {
		outcome = o.runCurrent(ctx, &doc, &state)
		if outcome != models.OutcomeStopped {
			return outcome
		}
	}
	o.phase(state.CurrentMilestone, len(doc.Milestones), state.PlanRevision, "complete")
	if err := o.files.Write(o.cfg.StopFile, "All milestones verified.\n"); err != nil {
		return models.OutcomeDroidFailed
	}
	return models.OutcomeStopped
}

// runCurrent applies every gate and transition for the next unfinished milestone.
func (o *MilestoneOrchestrator) runCurrent(ctx context.Context, doc *MilestoneDocument, state *models.MilestoneState) models.Outcome {
	n, exists := NextMilestone(*state, *doc)
	if !exists {
		return models.OutcomeReplanLimit
	}
	state.CurrentMilestone = n
	m, found := doc.Find(n)
	if !found {
		return models.OutcomeDroidFailed
	}
	if !state.Milestones[strconv.Itoa(n)].IntentChecked {
		if outcome := o.prepare(ctx, m, state); outcome != models.OutcomeStopped {
			return outcome
		}
	}
	outcome := o.execute(ctx, *state, n, len(doc.Milestones))
	if outcome == models.OutcomeDiverged {
		return o.replanDivergence(ctx, doc, state, n)
	}
	if outcome != models.OutcomeStopped {
		return outcome
	}
	return o.verify(ctx, doc, state, m)
}

// execute enforces the intent predicate before delegating to the inner loop.
func (o *MilestoneOrchestrator) execute(ctx context.Context, state models.MilestoneState, n, total int) models.Outcome {
	if !CanExecute(state, n) {
		fmt.Fprintf(o.terminal, "determined: milestone %d gate rejected approvedRevision\n", n)
		return models.OutcomeIntentLimit
	}
	o.phase(n, total, state.PlanRevision, "execute")
	return o.newInner(innerConfig(o.cfg)).Run(ctx)
}

// replanDivergence consumes the signal and starts a bounded forward replan.
func (o *MilestoneOrchestrator) replanDivergence(ctx context.Context, doc *MilestoneDocument, state *models.MilestoneState, n int) models.Outcome {
	reason, err := o.readDivergence()
	if err != nil {
		return models.OutcomeDroidFailed
	}
	if !o.replan(ctx, doc, state, n, reason) {
		return models.OutcomeReplanLimit
	}
	return models.OutcomeStopped
}

// verify removes the inner sentinel and records only independently verified work.
func (o *MilestoneOrchestrator) verify(ctx context.Context, doc *MilestoneDocument, state *models.MilestoneState, m Milestone) models.Outcome {
	if err := o.files.Remove(o.cfg.StopFile); err != nil {
		return models.OutcomeDroidFailed
	}
	o.phase(m.Number, len(doc.Milestones), state.PlanRevision, "verify")
	verdict := o.invoke(ctx, verifyMilestonePrompt(m, o.build(ctx)), "PASS", "REPLAN")
	if verdict.Token != "PASS" {
		return o.replanVerification(ctx, doc, state, m.Number, verdict.Rationale)
	}
	*state = MarkVerified(*state, m.Number)
	if !o.saveState(*state) {
		return models.OutcomeDroidFailed
	}
	o.checkpoint(ctx, m.Number, state.PlanRevision)
	return models.OutcomeStopped
}

// replanVerification supplies a stable reason when verifier output is malformed.
func (o *MilestoneOrchestrator) replanVerification(ctx context.Context, doc *MilestoneDocument, state *models.MilestoneState, n int, reason string) models.Outcome {
	if reason == "" {
		reason = "verification returned no valid verdict"
	}
	if !o.replan(ctx, doc, state, n, reason) {
		return models.OutcomeReplanLimit
	}
	return models.OutcomeStopped
}

// load validates the document and reconciles it with any persisted checkpoint.
func (o *MilestoneOrchestrator) load() (MilestoneDocument, models.MilestoneState, models.Outcome, bool) {
	c, e := o.files.Read(o.cfg.MilestonesFile)
	if e != nil {
		return MilestoneDocument{}, models.MilestoneState{}, models.OutcomeMissingFiles, false
	}
	d, e := ParseMilestones(c)
	if e != nil {
		fmt.Fprintf(o.terminal, "determined: invalid milestones: %v\n", e)
		return d, models.MilestoneState{}, models.OutcomeMissingFiles, false
	}
	s, found, e := LoadMilestoneState(o.files, o.cfg.MilestoneStateFile)
	if e != nil {
		return d, s, models.OutcomeDroidFailed, false
	}
	if !found {
		s = NewMilestoneState(d)
		if err := o.ignoreStateDirectory(); err != nil {
			return d, s, models.OutcomeDroidFailed, false
		}
		if !o.saveState(s) {
			return d, s, models.OutcomeDroidFailed, false
		}
	} else if !sameMilestones(s, d) {
		fmt.Fprintln(o.terminal, "determined: milestone document changed since last run; replanning state")
		s = mergeMilestoneState(s, d)
		if !o.saveState(s) {
			return d, s, models.OutcomeDroidFailed, false
		}
	}
	return d, s, models.OutcomeStopped, true
}

func (o *MilestoneOrchestrator) ignoreStateDirectory() error {
	content := ""
	if o.files.Exists(".gitignore") {
		var err error
		content, err = o.files.Read(".gitignore")
		if err != nil {
			return err
		}
	}
	for _, line := range strings.Split(content, "\n") {
		if strings.TrimSpace(line) == ".determined/" {
			return nil
		}
	}
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	return o.files.Write(".gitignore", content+".determined/\n")
}

// prepare elaborates and intent-checks a milestone until approved or capped.
func (o *MilestoneOrchestrator) prepare(ctx context.Context, m Milestone, s *models.MilestoneState) models.Outcome {
	for {
		o.phase(m.Number, len(s.Milestones), s.PlanRevision, "elaborate")
		if old, e := o.files.Read(o.cfg.StepsFile); e == nil {
			if err := o.files.Write(fmt.Sprintf(".determined/steps/milestone-%d.md", m.Number), old); err != nil {
				return models.OutcomeDroidFailed
			}
		}
		if err := o.files.Remove(o.cfg.DivergenceFile); err != nil {
			return models.OutcomeDroidFailed
		}
		if err := o.files.Remove(o.cfg.StepsFile); err != nil {
			return models.OutcomeDroidFailed
		}
		o.invoke(ctx, elaboratePrompt(m, s.PlanRevision))
		o.phase(m.Number, len(s.Milestones), s.PlanRevision, "intent-check")
		g := o.invoke(ctx, intentCheckPrompt(m), "PASS", "FAIL")
		if g.Token == "PASS" {
			*s = ApproveIntent(*s, m.Number)
			if !o.saveState(*s) {
				return models.OutcomeDroidFailed
			}
			return models.OutcomeStopped
		}
		v := s.Milestones[strconv.Itoa(m.Number)]
		v.IntentRetries++
		s.Milestones[strconv.Itoa(m.Number)] = v
		if !o.saveState(*s) {
			return models.OutcomeDroidFailed
		}
		if err := o.files.Append("FIXES.md", fmt.Sprintf("\n## Intent check (milestone %d, revision %d)\n%s\n", m.Number, s.PlanRevision, g.Rationale)); err != nil {
			return models.OutcomeDroidFailed
		}
		if o.cfg.MaxIntentRetries > 0 && v.IntentRetries > o.cfg.MaxIntentRetries {
			if err := o.files.Append("NOTES.md", "\nMilestone intent check retry cap reached.\n"); err != nil {
				return models.OutcomeDroidFailed
			}
			return models.OutcomeIntentLimit
		}
	}
}

// invoke runs one planning-tool prompt and parses an optional structured verdict.
func (o *MilestoneOrchestrator) invoke(ctx context.Context, p string, accepted ...string) GateVerdict {
	var b bytes.Buffer
	inv := models.Invocation{}
	if o.cfg.PlanTool != nil {
		inv = o.cfg.PlanTool.Invocation(p)
	}
	if inv.Binary == "" {
		inv = o.cfg.Tool.Invocation(p)
	}
	if e := o.runner.Run(ctx, inv, &b); e != nil {
		return GateVerdict{}
	}
	if len(accepted) == 0 {
		return GateVerdict{Token: "PASS"}
	}
	g, ok := ParseGateVerdict(b.String(), accepted...)
	if !ok {
		return GateVerdict{}
	}
	return g
}

// replan asks the planning tool to revise the current and later milestones.
func (o *MilestoneOrchestrator) replan(ctx context.Context, d *MilestoneDocument, s *models.MilestoneState, n int, reason string) bool {
	if o.cfg.MaxPlanRevisions > 0 && s.PlanRevision-1 >= o.cfg.MaxPlanRevisions {
		if err := o.files.Append("NOTES.md", "\nMilestone plan revision cap reached.\n"); err != nil {
			fmt.Fprintf(o.terminal, "determined: %v\n", err)
		}
		return false
	}
	o.phase(n, len(d.Milestones), s.PlanRevision, "replan")
	o.invoke(ctx, replanPrompt(n, reason, s.PlanRevision+1))
	content, e := o.files.Read(o.cfg.MilestonesFile)
	if e != nil {
		return false
	}
	next, e := ParseMilestones(content)
	if e != nil {
		return false
	}
	*s = Replan(*s, n)
	fresh := NewMilestoneState(next)
	for k := range s.Milestones {
		if _, ok := fresh.Milestones[k]; !ok {
			delete(s.Milestones, k)
		}
	}
	for k := range fresh.Milestones {
		status, ok := s.Milestones[k]
		if !ok {
			status = fresh.Milestones[k]
		}
		status.DefinitionHash = fresh.Milestones[k].DefinitionHash
		s.Milestones[k] = status
	}
	*d = next
	return o.saveState(*s)
}

// sameMilestones compares every execution-relevant milestone definition.
func sameMilestones(s models.MilestoneState, d MilestoneDocument) bool {
	if len(s.Milestones) != len(d.Milestones) {
		return false
	}
	for _, m := range d.Milestones {
		status, ok := s.Milestones[strconv.Itoa(m.Number)]
		if !ok || status.DefinitionHash != milestoneDefinitionHash(m) {
			return false
		}
	}
	return true
}

// mergeMilestoneState retains verification only for byte-equivalent definitions.
func mergeMilestoneState(old models.MilestoneState, d MilestoneDocument) models.MilestoneState {
	next := NewMilestoneState(d)
	next.PlanRevision = old.PlanRevision + 1
	for key, value := range old.Milestones {
		fresh, ok := next.Milestones[key]
		if ok && value.Verified && value.DefinitionHash == fresh.DefinitionHash {
			next.Milestones[key] = value
		}
	}
	if n, ok := NextMilestone(next, d); ok {
		next.CurrentMilestone = n
	}
	return next
}

// readDivergence consumes the worker's replan reason without hiding read failures.
func (o *MilestoneOrchestrator) readDivergence() (string, error) {
	c, err := o.files.Read(o.cfg.DivergenceFile)
	if err != nil {
		return "", err
	}
	if err := o.files.Remove(o.cfg.DivergenceFile); err != nil {
		return "", err
	}
	return c, nil
}

// build runs the default Go verification commands through the injected runner.
func (o *MilestoneOrchestrator) build(ctx context.Context) string {
	if !o.files.Exists("go.mod") {
		return "No automated check was run."
	}
	var output bytes.Buffer
	if err := o.runner.Run(ctx, models.Invocation{Binary: "go", Args: []string{"build", "./..."}}, &output); err != nil {
		return output.String() + "\n" + err.Error()
	}
	if err := o.runner.Run(ctx, models.Invocation{Binary: "go", Args: []string{"test", "./..."}}, &output); err != nil {
		return output.String() + "\n" + err.Error()
	}
	return output.String()
}

// checkpoint commits a verified milestone through the injected runner.
func (o *MilestoneOrchestrator) checkpoint(ctx context.Context, n, revision int) {
	if !o.cfg.GitCheckpoint {
		return
	}
	if err := o.runner.Run(ctx, models.Invocation{Binary: "git", Args: []string{"rev-parse", "--is-inside-work-tree"}}, io.Discard); err != nil {
		return
	}
	if err := o.runner.Run(ctx, models.Invocation{Binary: "git", Args: []string{"add", "-A"}}, io.Discard); err != nil {
		return
	}
	message := fmt.Sprintf("determined: milestone %d verified (revision %d)", n, revision)
	if err := o.runner.Run(ctx, models.Invocation{Binary: "git", Args: []string{"commit", "--allow-empty", "-m", message}}, io.Discard); err != nil {
		fmt.Fprintf(o.terminal, "determined: milestone checkpoint failed: %v\n", err)
	}
}

// saveState reports persistence failures and prevents unsafe continuation.
func (o *MilestoneOrchestrator) saveState(state models.MilestoneState) bool {
	if err := SaveMilestoneState(o.files, o.cfg.MilestoneStateFile, state); err != nil {
		fmt.Fprintf(o.terminal, "determined: %v\n", err)
		return false
	}
	return true
}

// phase publishes milestone progress when a status reporter is available.
func (o *MilestoneOrchestrator) phase(n, total, revision int, p string) {
	if s, ok := o.status.(interface {
		SetMilestone(models.MilestoneProgress)
	}); ok {
		s.SetMilestone(models.MilestoneProgress{Current: n, Total: total, Revision: revision, Phase: p})
	}
}

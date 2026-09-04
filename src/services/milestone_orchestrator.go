package services

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"

	"determined/src/models"
)

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

func NewMilestoneOrchestrator(r CommandRunner, f FileStore, c Clock, l LogSink, t io.Writer, cfg models.Config, n func(models.Config) *Orchestrator) *MilestoneOrchestrator {
	return &MilestoneOrchestrator{runner: r, files: f, clock: c, logs: l, terminal: t, cfg: cfg, newInner: n}
}
func (o *MilestoneOrchestrator) WithStatusReporter(s ExecStatusReporter) *MilestoneOrchestrator {
	o.status = s
	return o
}

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
func InnerConfig(cfg models.Config) models.Config { return innerConfig(cfg) }

func (o *MilestoneOrchestrator) Run(ctx context.Context) models.Outcome {
	doc, state, outcome, ok := o.load()
	if !ok {
		return outcome
	}
	for !AllVerified(state) {
		n, exists := NextMilestone(state, doc)
		if !exists {
			return models.OutcomeReplanLimit
		}
		state.CurrentMilestone = n
		m, _ := doc.Find(n)
		if !state.Milestones[strconv.Itoa(n)].IntentChecked {
			if out := o.prepare(ctx, m, &state); out != models.OutcomeStopped {
				return out
			}
		}
		if !CanExecute(state, n) {
			fmt.Fprintf(o.terminal, "determined: milestone %d gate rejected approvedRevision\n", n)
			return models.OutcomeIntentLimit
		}
		o.phase(n, len(doc.Milestones), state.PlanRevision, "execute")
		inner := o.newInner(innerConfig(o.cfg))
		out := inner.Run(ctx)
		if out == models.OutcomeDiverged {
			if !o.replan(ctx, &doc, &state, n, o.divergence()) {
				return models.OutcomeReplanLimit
			}
			continue
		}
		if out != models.OutcomeStopped {
			return out
		}
		o.files.Remove(o.cfg.StopFile)
		o.phase(n, len(doc.Milestones), state.PlanRevision, "verify")
		verdict := o.invoke(ctx, verifyMilestonePrompt(m, o.build(ctx)), "PASS", "REPLAN")
		if verdict.Token != "PASS" {
			reason := verdict.Rationale
			if reason == "" {
				reason = "verification returned no valid verdict"
			}
			if !o.replan(ctx, &doc, &state, n, reason) {
				return models.OutcomeReplanLimit
			}
			continue
		}
		MarkVerified(&state, n)
		SaveMilestoneState(o.files, o.cfg.MilestoneStateFile, state)
		o.checkpoint(ctx, n, state.PlanRevision)
	}
	o.phase(state.CurrentMilestone, len(doc.Milestones), state.PlanRevision, "complete")
	o.files.Write(o.cfg.StopFile, "All milestones verified.\n")
	return models.OutcomeStopped
}

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
		o.ignoreStateDirectory()
		SaveMilestoneState(o.files, o.cfg.MilestoneStateFile, s)
	} else if !sameMilestones(s, d) {
		fmt.Fprintln(o.terminal, "determined: milestone document changed since last run; replanning state")
		s = mergeMilestoneState(s, d)
		SaveMilestoneState(o.files, o.cfg.MilestoneStateFile, s)
	}
	return d, s, models.OutcomeStopped, true
}

func (o *MilestoneOrchestrator) ignoreStateDirectory() {
	content, _ := o.files.Read(".gitignore")
	for _, line := range strings.Split(content, "\n") {
		if strings.TrimSpace(line) == ".determined/" {
			return
		}
	}
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	o.files.Write(".gitignore", content+".determined/\n")
}
func (o *MilestoneOrchestrator) prepare(ctx context.Context, m Milestone, s *models.MilestoneState) models.Outcome {
	for {
		o.phase(m.Number, len(s.Milestones), s.PlanRevision, "elaborate")
		if old, e := o.files.Read(o.cfg.StepsFile); e == nil {
			o.files.Write(fmt.Sprintf(".determined/steps/milestone-%d.md", m.Number), old)
		}
		o.files.Remove(o.cfg.DivergenceFile)
		o.files.Remove(o.cfg.StepsFile)
		o.invoke(ctx, elaboratePrompt(m, s.PlanRevision))
		o.phase(m.Number, len(s.Milestones), s.PlanRevision, "intent-check")
		g := o.invoke(ctx, intentCheckPrompt(m), "PASS", "FAIL")
		if g.Token == "PASS" {
			ApproveIntent(s, m.Number)
			SaveMilestoneState(o.files, o.cfg.MilestoneStateFile, *s)
			return models.OutcomeStopped
		}
		v := s.Milestones[strconv.Itoa(m.Number)]
		v.IntentRetries++
		s.Milestones[strconv.Itoa(m.Number)] = v
		o.files.Append("FIXES.md", fmt.Sprintf("\n## Intent check (milestone %d, revision %d)\n%s\n", m.Number, s.PlanRevision, g.Rationale))
		if o.cfg.MaxIntentRetries > 0 && v.IntentRetries > o.cfg.MaxIntentRetries {
			o.files.Append("NOTES.md", "\nMilestone intent check retry cap reached.\n")
			return models.OutcomeIntentLimit
		}
	}
}
func (o *MilestoneOrchestrator) invoke(ctx context.Context, p string, accepted ...string) GateVerdict {
	var b bytes.Buffer
	inv := o.cfg.PlanTool.Invocation(p)
	if inv.Binary == "" {
		inv = o.cfg.Tool.Invocation(p)
	}
	if e := o.runner.Run(ctx, inv, &b); e != nil {
		return GateVerdict{}
	}
	if len(accepted) == 0 {
		return GateVerdict{Token: "PASS"}
	}
	g, _ := ParseGateVerdict(b.String(), accepted...)
	return g
}
func (o *MilestoneOrchestrator) replan(ctx context.Context, d *MilestoneDocument, s *models.MilestoneState, n int, reason string) bool {
	if o.cfg.MaxPlanRevisions > 0 && s.PlanRevision-1 >= o.cfg.MaxPlanRevisions {
		o.files.Append("NOTES.md", "\nMilestone plan revision cap reached.\n")
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
	Replan(s, n)
	fresh := NewMilestoneState(next)
	for k := range s.Milestones {
		if _, ok := fresh.Milestones[k]; !ok {
			delete(s.Milestones, k)
		}
	}
	for k := range fresh.Milestones {
		if _, ok := s.Milestones[k]; !ok {
			s.Milestones[k] = models.MilestoneStatus{}
		}
	}
	*d = next
	SaveMilestoneState(o.files, o.cfg.MilestoneStateFile, *s)
	return true
}

func sameMilestones(s models.MilestoneState, d MilestoneDocument) bool {
	if len(s.Milestones) != len(d.Milestones) {
		return false
	}
	for _, m := range d.Milestones {
		if _, ok := s.Milestones[strconv.Itoa(m.Number)]; !ok {
			return false
		}
	}
	return true
}

func mergeMilestoneState(old models.MilestoneState, d MilestoneDocument) models.MilestoneState {
	next := NewMilestoneState(d)
	next.PlanRevision = old.PlanRevision + 1
	for key, value := range old.Milestones {
		if _, ok := next.Milestones[key]; ok && value.Verified {
			next.Milestones[key] = value
		}
	}
	if n, ok := NextMilestone(next, d); ok {
		next.CurrentMilestone = n
	}
	return next
}
func (o *MilestoneOrchestrator) divergence() string {
	c, _ := o.files.Read(o.cfg.DivergenceFile)
	o.files.Remove(o.cfg.DivergenceFile)
	return c
}
func (o *MilestoneOrchestrator) build(ctx context.Context) string {
	if !o.files.Exists("go.mod") {
		return "No automated check was run."
	}
	c := exec.CommandContext(ctx, "sh", "-c", "go build ./... && go test ./...")
	b, e := c.CombinedOutput()
	if e != nil {
		return string(b) + "\n" + e.Error()
	}
	return string(b)
}

func (o *MilestoneOrchestrator) checkpoint(ctx context.Context, n, revision int) {
	if !o.cfg.GitCheckpoint {
		return
	}
	inside := exec.CommandContext(ctx, "git", "rev-parse", "--is-inside-work-tree")
	if err := inside.Run(); err != nil {
		return
	}
	if err := exec.CommandContext(ctx, "git", "add", "-A").Run(); err != nil {
		return
	}
	message := fmt.Sprintf("determined: milestone %d verified (revision %d)", n, revision)
	if err := exec.CommandContext(ctx, "git", "commit", "--allow-empty", "-m", message).Run(); err != nil {
		fmt.Fprintf(o.terminal, "determined: milestone checkpoint failed: %v\n", err)
	}
}
func (o *MilestoneOrchestrator) phase(n, total, revision int, p string) {
	if s, ok := o.status.(interface {
		SetMilestone(models.MilestoneProgress)
	}); ok {
		s.SetMilestone(models.MilestoneProgress{Current: n, Total: total, Revision: revision, Phase: p})
	}
}

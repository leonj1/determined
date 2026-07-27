// Command hangrepro serves the real interactive status page wired exactly like
// a production `-interactive -plan -exec` session, but drives it from a stubbed
// execute loop instead of a real AI tool. It exists to reproduce and measure
// the "page hangs during execution" report: pair it with
// tests/status_page_longtask_probe.js, which watches the page in headless
// Chrome and reports main-thread long tasks.
//
// Flags select the traffic shape:
//
//	-raw           bypass TraceToolOutput, streaming raw stream-json lines to
//	               the page (the pre-PR#39 behavior)
//	-rate          stream-json events per second (default 20)
//	-result-bytes  size of each tool_result payload (default 300000)
//	-duration      how long to stream before settling the run (default 60s)
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"determined/src/clients"
	"determined/src/models"
	"determined/src/services"
)

func main() {
	raw := flag.Bool("raw", false, "stream raw stream-json lines (pre-fix behavior)")
	rate := flag.Int("rate", 20, "stream-json events per second")
	resultBytes := flag.Int("result-bytes", 300000, "tool_result payload size in bytes")
	duration := flag.Duration("duration", 60*time.Second, "how long to stream")
	flag.Parse()

	clock := clients.NewSystemClock()
	status := services.NewPlanStatusService(
		clock,
		models.GitContext{Remote: "github.com/example/app", Branch: "main"},
		models.ToolIdentity{Name: "claude", Model: "claude-sonnet-5"},
		services.NewCircularLogBuffer(2000),
	)
	historyPath := filepath.Join(os.TempDir(), fmt.Sprintf("hangrepro-logs-%d.db", os.Getpid()))
	history, err := clients.NewSQLiteLogStore(historyPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hangrepro: %v\n", err)
		os.Exit(1)
	}
	defer func() {
		history.Close()
		for _, name := range []string{historyPath, historyPath + "-wal", historyPath + "-shm"} {
			os.Remove(name)
		}
	}()
	status.WithLogHistory(history)

	chat := services.NewChatService(status, clock)
	server := clients.NewPlanStatusServer(status, status, status, clock).
		WithLogSource(status).WithChatResponder(chat).
		WithTaskControl(status).WithStallChoice(status)
	if err := server.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "hangrepro: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("hangrepro: status page at %s\n", server.URL())

	seedPlanningPhase(status)
	streamExecution(status, *raw, *rate, *resultBytes, *duration)

	fmt.Println("hangrepro: streaming finished; page still serving — ctrl-c to exit")
	select {}
}

// seedPlanningPhase walks the session through a completed planning run so the
// page carries a production-shaped snapshot: a plan with mermaid, tables and
// fenced code, recommended tests, and a parsed step list.
func seedPlanningPhase(status *services.PlanStatusService) {
	status.Start()
	status.SetGoal("Add authenticated dashboards with per-tenant metrics, streaming updates, and an audit trail.")
	status.SetPlan(syntheticPlan())
	status.SetTests(syntheticTests())
	status.SetTaskSteps(syntheticTaskSteps())
	for _, message := range []string{
		"writing planning goal", "planning project", "recommending tests",
		"assessing plan", "refining plan",
	} {
		status.AddStep(message)
	}
	status.Finish(true)
	status.OfferImplement()
}

// streamExecution runs stubbed exec iterations until the duration elapses,
// emitting stream-json events through the same append path production uses.
func streamExecution(status *services.PlanStatusService, raw bool, rate, resultBytes int, duration time.Duration) {
	status.StartExecution()
	deadline := time.Now().Add(duration)
	interval := time.Second / time.Duration(rate)
	step := 0
	for time.Now().Before(deadline) {
		step++
		status.AddStep(fmt.Sprintf("executing step %d: wire the tenant metrics service", step))
		entry := status.BeginExecLogEntry(fmt.Sprintf("executing step %d: wire the tenant metrics service", step))
		_, cancel := context.WithCancel(context.Background())
		status.BeginTask(cancel)
		streamIteration(status, raw, resultBytes, interval, minTime(deadline, time.Now().Add(15*time.Second)))
		status.EndTask()
		status.SettleExecLogEntryAt(entry, models.EntryStateOK)
		cancel()
	}
	status.FinishExecution(true)
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// streamIteration emits one invocation's worth of events: tool calls, huge
// tool results, and assistant text, at the configured rate.
func streamIteration(status *services.PlanStatusService, raw bool, resultBytes int, interval time.Duration, until time.Time) {
	n := 0
	for time.Now().Before(until) {
		n++
		line := eventLine(n, resultBytes)
		if !raw {
			line = services.TraceToolOutput(line)
		}
		status.AppendExecLogOutput(line + "\n")
		time.Sleep(interval)
	}
}

// eventLine fabricates the n-th stream-json event of an invocation, cycling
// through the shapes the Claude CLI emits. Every 4th event is a tool_result
// carrying a payload of resultBytes characters — the shape that froze the
// page: whole file bodies on a single line.
func eventLine(n, resultBytes int) string {
	switch n % 4 {
	case 0:
		return marshal(map[string]any{
			"type": "user",
			"message": map[string]any{
				"content": []map[string]any{{
					"type":    "tool_result",
					"content": strings.Repeat("x", resultBytes),
				}},
			},
		})
	case 1:
		return marshal(map[string]any{
			"type": "assistant",
			"message": map[string]any{
				"content": []map[string]any{{
					"type": "tool_use", "name": "Read",
					"input": map[string]any{"file_path": fmt.Sprintf("/src/services/metrics_%d.go", n)},
				}},
			},
		})
	case 2:
		return marshal(map[string]any{
			"type": "assistant",
			"message": map[string]any{
				"content": []map[string]any{{
					"type": "text",
					"text": fmt.Sprintf("Now updating the aggregation window for shard %d.", n),
				}},
			},
		})
	default:
		return marshal(map[string]any{
			"type": "assistant",
			"message": map[string]any{
				"content": []map[string]any{{
					"type": "tool_use", "name": "Bash",
					"input": map[string]any{"command": fmt.Sprintf("go test ./... # pass %d", n)},
				}},
			},
		})
	}
}

func marshal(event map[string]any) string {
	body, err := json.Marshal(event)
	if err != nil {
		return "{}"
	}
	return string(body)
}

func syntheticPlan() string {
	var b strings.Builder
	b.WriteString("# PLAN: Tenant Metrics Dashboard\n\n## Goal\n\nServe per-tenant metrics with streaming updates.\n\n")
	b.WriteString("## Architecture\n\n```mermaid\nsequenceDiagram\nBrowser->>API: GET /metrics\nAPI->>Store: query window\nStore-->>API: rows\nAPI-->>Browser: SSE stream\n```\n\n")
	b.WriteString("## Endpoints\n\n| Route | Method | Purpose |\n|---|---|---|\n")
	for i := 1; i <= 12; i++ {
		fmt.Fprintf(&b, "| /api/metrics/%d | GET | window %d aggregation |\n", i, i)
	}
	b.WriteString("\n")
	for i := 1; i <= 10; i++ {
		fmt.Fprintf(&b, "## Section %d\n\nDetail paragraph for section %d covering the service boundary, the client interface, and its fake.\n\n```go\nfunc (s *Service%d) Window(ctx context.Context) ([]Row, error) {\n\treturn s.store.Query(ctx)\n}\n```\n\n", i, i, i)
	}
	return b.String()
}

func syntheticTests() string {
	return "## Journey\n\n- login lands on the dashboard\n- metrics stream updates live\n\n## BDD\n\n```gherkin\nFeature: metrics\n  Scenario: live updates\n    Given a logged-in tenant\n    When a new datapoint arrives\n    Then the chart updates within 2 seconds\n```\n"
}

func syntheticTaskSteps() []models.TaskStep {
	steps := make([]models.TaskStep, 0, 12)
	for i := 1; i <= 12; i++ {
		steps = append(steps, models.TaskStep{
			Text:     fmt.Sprintf("Step %d: implement the `MetricsService` window %d with **bounded** queries", i, i),
			Purpose:  fmt.Sprintf("Isolates aggregation window %d behind the service interface.", i),
			DoneWhen: fmt.Sprintf("`go test ./tests -run Window%d` passes.", i),
		})
	}
	return steps
}

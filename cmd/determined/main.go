package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"determined/src/clients"
	"determined/src/models"
	"determined/src/services"
)

// prompt is the hardcoded instruction handed to the AI coding tool each
// iteration, ported verbatim from the original bash loop.
const prompt = "Read PLAN.md and STEPS.md. Find the first step that has needs to be completed. " +
	"Implement that step. Mark the step completed when you are done. Only work on one step. " +
	"When there are no more steps then create STOP.md"

func main() {
	cfg, logDir := parseConfig()
	clock := clients.NewSystemClock()
	orchestrator := services.NewOrchestrator(
		clients.NewExecCommandRunner(),
		clients.NewOsStopSignal(),
		clock,
		clients.NewFileLogSink(logDir, clock),
		os.Stdout,
		cfg,
	)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	outcome := orchestrator.Run(ctx)
	fmt.Fprintf(os.Stderr, "\ndetermined: %s\n", outcome)
	os.Exit(outcome.ExitCode())
}

// parseConfig reads the limit flags and assembles the hardcoded run config.
func parseConfig() (models.Config, string) {
	budget := flag.Duration("max-duration", time.Hour,
		"wall-clock budget, checked between iterations; 0 means unlimited")
	logDir := flag.String("log-dir", "logs", "directory for per-iteration log files")
	auto := flag.String("auto", "medium",
		"droid autonomy level passed as --auto; required for unattended runs (low|medium|high)")
	flag.Parse()

	cfg := models.Config{
		StopFile:   "STOP.md",
		Invocation: models.Invocation{Binary: "droid", Args: []string{"exec", "--auto", *auto, prompt}},
		Budget:     *budget,
	}
	return cfg, *logDir
}

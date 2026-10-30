package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/ocelhq/ocel/cli/internal/jobinstall"
	"github.com/ocelhq/ocel/pkg/runner"
	"github.com/ocelhq/ocel/pkg/runner/agent"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Args[1:], os.Getenv, os.Stderr)
	stop()
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "ocel-job:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, getenv func(string) string, stderr io.Writer) error {
	flags := flag.NewFlagSet("ocel-job", flag.ContinueOnError)
	flags.SetOutput(stderr)
	runnerURL := flags.String("runner", "", "The URL of the runner whose job this agent runs")
	dir := flags.String("dir", "/workspace/job", "The directory the job's source and home are written to")
	timeout := flags.Duration("timeout", 0, "How long the job may run before it is stopped")
	ocel := flags.String("ocel", "ocel", "The ocel binary the job runs")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() > 0 {
		return fmt.Errorf("takes no arguments, got %q", flags.Args())
	}
	token := getenv(runner.TokenEnvVar)
	if token == "" {
		return fmt.Errorf("$%s holds no job token", runner.TokenEnvVar)
	}
	if *runnerURL == "" {
		return errors.New("--runner names no runner to run the job for")
	}
	binary, err := exec.LookPath(*ocel)
	if err != nil {
		return fmt.Errorf("find the ocel binary the job runs: %w", err)
	}
	return agent.Run(ctx, agent.Config{
		Runner: *runnerURL, Token: token, Ocel: binary, Dir: *dir, Timeout: *timeout,
		Env: os.Environ(), FindInstalls: jobinstall.Find,
	})
}

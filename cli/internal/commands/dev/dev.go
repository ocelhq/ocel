package dev

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/dev"
	"github.com/ocelhq/ocel/cli/internal/devresources/docker"
)

type Dependencies struct {
	commands.Invocation
	OpenDocker docker.OpenFunc
}

func NewCommand(dependencies Dependencies) *cobra.Command {
	var reset bool
	cmd := &cobra.Command{
		Use:   "dev -- <command> [args...]",
		Short: "Run your project in development mode",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runOnBus(cmd, dependencies, "ocel dev", args, func(ctx context.Context, opts dev.Options, _ string) error {
				return dev.Run(ctx, opts, reset)
			})
		},
	}
	cmd.Flags().BoolVar(&reset, "reset", false, "Wipe the data this project's dev resources have kept, then start from empty ones")
	return commands.DeclareMutating(commands.ShareTerminalWithChild(cmd))
}

func runOnBus(cmd *cobra.Command, dependencies Dependencies, command string, args []string, body func(ctx context.Context, opts dev.Options, cwd string) error) (err error) {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("determine working directory: %w", err)
	}
	opts, err := loadOptions(cmd.Context(), dependencies, cwd, args, cmd.OutOrStdout(), cmd.ErrOrStderr(), cmd.InOrStdin())
	if err != nil {
		return err
	}
	ctx, begun, err := dependencies.Events.Begin(cmd.Context(), command, opts.Project.Dir)
	if err != nil {
		return err
	}
	defer begun.End(&err)
	opts.Run = begun
	return body(ctx, opts, cwd)
}

func loadOptions(ctx context.Context, dependencies Dependencies, cwd string, command []string, stdout, stderr io.Writer, stdin io.Reader) (dev.Options, error) {
	cfg, err := dependencies.LoadOptionalProject(ctx, cwd)
	if err != nil {
		return dev.Options{}, err
	}
	return dev.Options{
		Project:         cfg,
		Command:         command,
		OpenDocker:      dependencies.OpenDocker,
		RecordEvent:     dependencies.RecordEvent,
		Stdin:           stdin,
		Stdout:          stdout,
		Stderr:          stderr,
		StdinIsTerminal: dependencies.StdinIsTerminal(stdin),
	}, nil
}

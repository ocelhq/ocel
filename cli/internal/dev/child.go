package dev

import (
	"context"
	"errors"
	"os"
	"os/exec"

	"github.com/ocelhq/ocel/cli/internal/childprocess"
	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/exitcode"
)

func startChild(ctx context.Context, opts Options, env map[string]string) (*childprocess.Child, error) {
	cmd := exec.CommandContext(ctx, opts.Command[0], opts.Command[1:]...)
	cmd.Env = applyEnv(os.Environ(), env)
	cmd.Stdin = opts.Stdin
	cmd.Stdout = opts.Stdout
	cmd.Stderr = opts.Stderr
	return childprocess.Start(ctx, cmd, opts.Stdin, opts.StdinIsTerminal)
}

func runChild(ctx context.Context, opts Options, env map[string]string) error {
	child, err := startChild(ctx, opts, env)
	if err != nil {
		return err
	}
	return exitError(ctx, child.Wait())
}

func exitError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return &exitcode.ExitError{Code: exitcode.Interrupt}
	}
	if err == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return &clierror.Error{Code: clierror.CodeDevCommandFailed, Cause: &exitcode.ExitError{Code: childprocess.ExitCode(exitErr)}}
	}
	return err
}

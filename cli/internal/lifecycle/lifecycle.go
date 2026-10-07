package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/ocelhq/ocel/cli/internal/childprocess"
	"github.com/ocelhq/ocel/cli/internal/livedir"
	"github.com/ocelhq/ocel/pkg/processenv"
)

type Command struct {
	Shell   string
	Dir     string
	Env     map[string]string
	Live    map[string]string
	Timeout time.Duration
	Stdout  io.Writer
	Stderr  io.Writer
}

type FailedError struct {
	Command  string
	ExitCode int
}

func (e *FailedError) Error() string {
	return fmt.Sprintf("%q exited with code %d", e.Command, e.ExitCode)
}

type TimeoutError struct {
	Command string
	After   time.Duration
}

func (e *TimeoutError) Error() string {
	return fmt.Sprintf("%q ran past its timeout of %s and was stopped", e.Command, e.After)
}

func Run(ctx context.Context, c Command) (err error) {
	runCtx := ctx
	if c.Timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, c.Timeout)
		defer cancel()
	}

	env := make(map[string]string, len(c.Env)+1)
	for key, value := range c.Env {
		env[key] = value
	}
	if len(c.Live) > 0 {
		dir, err := livedir.Write("", "ocel-live-", c.Live)
		if err != nil {
			return err
		}
		defer func() { err = errors.Join(err, livedir.Remove(dir)) }()
		env[processenv.LiveDirEnvVar] = dir
	}

	cmd := newShellCommand(runCtx, c.Shell)
	cmd.Dir = c.Dir
	cmd.Env = environment(os.Environ(), env)
	cmd.Stdout = c.Stdout
	cmd.Stderr = c.Stderr
	child, err := childprocess.Start(runCtx, cmd, nil, false)
	if err != nil {
		return fmt.Errorf("start %q: %w", c.Shell, err)
	}
	waitErr := child.Wait()

	switch {
	case waitErr == nil:
		return nil
	case ctx.Err() != nil:
		return fmt.Errorf("%q was stopped: %w", c.Shell, ctx.Err())
	case runCtx.Err() != nil:
		return &TimeoutError{Command: c.Shell, After: c.Timeout}
	}
	var exited *exec.ExitError
	if errors.As(waitErr, &exited) {
		return &FailedError{Command: c.Shell, ExitCode: childprocess.ExitCode(exited)}
	}
	return waitErr
}

func environment(base []string, overrides map[string]string) []string {
	merged := make([]string, 0, len(base)+len(overrides))
	for _, kv := range base {
		key, _, _ := strings.Cut(kv, "=")
		if _, overridden := overrides[key]; overridden || strings.HasPrefix(key, processenv.ResourceEnvVarPrefix) || key == processenv.LiveDirEnvVar {
			continue
		}
		merged = append(merged, kv)
	}
	for key, value := range overrides {
		merged = append(merged, key+"="+value)
	}
	return merged
}

package ssh

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"

	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/tracing"
)

type Local struct {
	Hostname string
	User     string
	Path     string
}

func (l Local) Destination() Destination {
	return Destination{Written: l.Hostname, Port: 22, User: l.User}
}

func (l Local) command(ctx context.Context, command string) *tracing.Cmd {
	cmd := tracing.CommandContext(ctx, "/bin/sh", "-c", command)
	cmd.Env = os.Environ()
	if l.Path != "" {
		cmd.Env = append(cmd.Env, "PATH="+l.Path)
	}
	return cmd
}

func (l Local) Stream(ctx context.Context, command string, stdin io.Reader) (Result, error) {
	stdout, stderr, code, err := collectOutput(l.command(ctx, command), stdin)
	if err != nil {
		return Result{}, refusal.Refuse(refusal.CodeNotReady, "%s on this box: %s", l.User, terse(failure{err: err, stderr: stderr}))
	}
	return Result{Stdout: stdout, Stderr: stderr, Code: code}, nil
}

func (l Local) Run(ctx context.Context, command string) (string, error) {
	result, err := l.Stream(ctx, command, nil)
	if err != nil {
		return "", err
	}
	if result.Code != 0 {
		return "", refusal.Refuse(refusal.CodeNotReady, "%s on this box: %s", l.User, terse(result.problem(command)))
	}
	return result.Stdout, nil
}

func (l Local) RunLines(ctx context.Context, command string, each func(Line) error) error {
	commandCtx, stop := context.WithCancel(ctx)
	defer stop()
	cmd := l.command(commandCtx, command)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return refusal.Refuse(refusal.CodeNotReady, "%s on this box: %s", l.User, err)
	}
	var (
		mu          sync.Mutex
		callbackErr error
		readers     sync.WaitGroup
	)
	for pipe, reader := range map[Pipe]io.Reader{Stdout: stdout, Stderr: stderr} {
		readers.Go(func() {
			_ = readLines(reader, func(text string) bool {
				mu.Lock()
				defer mu.Unlock()
				if callbackErr != nil {
					return false
				}
				if err := each(Line{Pipe: pipe, Text: text}); err != nil {
					callbackErr = err
					stop()
					return false
				}
				return true
			})
		})
	}
	readers.Wait()
	waitErr := cmd.Wait()
	if callbackErr != nil {
		return callbackErr
	}
	code, err := exitStatus(waitErr)
	switch {
	case err != nil && ctx.Err() != nil:
		return ctx.Err()
	case err != nil:
		return refusal.Refuse(refusal.CodeNotReady, "%s on this box: %s", l.User, err)
	case code != 0:
		return refusal.Refuse(refusal.CodeNotReady, "%s on this box: %s exited %d", l.User, command, code)
	}
	return nil
}

func (l Local) Preflight(ctx context.Context) (Facts, error) {
	rendered, err := l.Run(ctx, survey)
	if err != nil {
		return Facts{}, err
	}
	facts := readFacts(rendered)
	return facts, met(facts, l.User+" on this box")
}

func (l Local) ForwardPort(_ context.Context, remote string) (string, func(), error) {
	if remote == "" {
		return "", nil, errors.New("no address to reach")
	}
	return remote, func() {}, nil
}

func (Local) ForwardSpans() string { return "" }

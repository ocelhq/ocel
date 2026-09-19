package dockertest

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/ocelhq/ocel/cli/internal/devstack/docker"
)

type Engine struct {
	mu sync.Mutex

	Specs    []docker.Spec
	Execs    [][]string
	Stopped  []string
	Wiped    []map[string]string
	Closed   bool
	Answer   func(argv []string) (string, error)
	RunError error
}

func (e *Engine) Run(_ context.Context, spec docker.Spec) (docker.Container, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.RunError != nil {
		return docker.Container{}, e.RunError
	}
	e.Specs = append(e.Specs, spec)
	return docker.Container{ID: spec.Name, Addr: fmt.Sprintf("127.0.0.1:%d", 54000+len(e.Specs))}, nil
}

func (e *Engine) Exec(_ context.Context, _ string, argv ...string) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.Execs = append(e.Execs, argv)
	if e.Answer != nil {
		return e.Answer(argv)
	}
	return "", nil
}

func (e *Engine) Stop(_ context.Context, id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.Stopped = append(e.Stopped, id)
	return nil
}

func (e *Engine) RemoveVolumes(_ context.Context, labels map[string]string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.Wiped = append(e.Wiped, labels)
	return nil
}

func (e *Engine) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.Closed = true
	return nil
}

func (e *Engine) Ran(program string) [][]string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var ran [][]string
	for _, argv := range e.Execs {
		if argv[0] == program {
			ran = append(ran, argv)
		}
	}
	return ran
}

func (e *Engine) Opener() docker.Opener {
	return func(context.Context) (docker.Engine, error) { return e, nil }
}

func Joined(argv []string) string { return strings.Join(argv, " ") }

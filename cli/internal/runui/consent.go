package runui

import (
	"context"
	"fmt"
	"io"

	"github.com/ocelhq/ocel/cli/internal/prompt"
)

type gate struct {
	command     string
	class       Consent
	yes         bool
	dry         bool
	interactive bool
	unattended  string
	in          io.Reader
	out         io.Writer
}

func (g gate) refuse() error {
	if g.class != PlanFirst || g.dry || g.yes || g.interactive {
		return nil
	}
	return g.blocked()
}

func (g gate) blocked() error {
	remedy := g.unattended
	if remedy == "" {
		remedy = "pass --yes"
	}
	return fmt.Errorf("`%s` needs a terminal to confirm the plan it shows before applying it; to run it unattended, %s", g.command, remedy)
}

func (g gate) guard(ctx context.Context, question string) (bool, error) {
	if g.yes || !g.interactive {
		return true, nil
	}
	return g.decide(prompt.New(g.out, g.in).Confirm(ctx, question))
}

func (g gate) consent(ctx context.Context, ask func(prompt.Prompter) (bool, error)) (bool, error) {
	if g.yes {
		return true, nil
	}
	if !g.interactive {
		return false, g.blocked()
	}
	return g.decide(ask(prompt.New(g.out, g.in)))
}

func (s *Session) Interactive() bool { return s.gate.interactive }

func (s *Session) Asking() bool { return s.gate.interactive && !s.gate.yes }

func (s *Session) Guard(ctx context.Context, question string) (bool, error) {
	resume := s.Suspend()
	defer resume()
	return s.gate.guard(ctx, question)
}

func (s *Session) Consent(ctx context.Context, question string) (bool, error) {
	if s.settled() {
		return true, nil
	}
	resume := s.Suspend()
	defer resume()
	return s.gate.consent(ctx, func(p prompt.Prompter) (bool, error) {
		return p.Confirm(ctx, question)
	})
}

func (s *Session) ConsentByName(ctx context.Context, label, name string) (bool, error) {
	if s.settled() {
		return true, nil
	}
	resume := s.Suspend()
	defer resume()
	return s.gate.consent(ctx, func(p prompt.Prompter) (bool, error) {
		return p.Phrase(ctx, label, name)
	})
}

func (s *Session) settled() bool {
	return len(s.shown.GetGroups()) > 0 && !Mutates(s.shown)
}

func (g gate) decide(granted bool, err error) (bool, error) {
	if err != nil || granted {
		return granted, err
	}
	fmt.Fprintln(g.out, "Aborted.")
	return false, nil
}

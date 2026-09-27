package consent

import (
	"context"
	"fmt"
	"io"

	"github.com/ocelhq/ocel/cli/internal/prompt"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	planv1 "github.com/ocelhq/ocel/pkg/proto/common/plan/v1"
)

type Class int

const (
	Convergent Class = iota
	PlanFirst
)

type Scope interface {
	Hold(waiting *streamv1.WaitingEvent) (resume func(reason string))
	Say(message string)
}

type Gate struct {
	Command     string
	Class       Class
	Yes         bool
	Dry         bool
	Interactive bool
	Unattended  string
	In          io.Reader
	Out         io.Writer
}

func (g Gate) Asking() bool { return g.Interactive && !g.Yes }

func (g Gate) Refuse() error {
	if g.Class != PlanFirst || g.Dry || g.Yes || g.Interactive {
		return nil
	}
	return g.blocked()
}

func (g Gate) blocked() error {
	remedy := g.Unattended
	if remedy == "" {
		remedy = "pass --yes"
	}
	return fmt.Errorf("`%s` needs a terminal to confirm the plan it shows before applying it; to run it unattended, %s", g.Command, remedy)
}

func (g Gate) Guard(ctx context.Context, scope Scope, question string) (bool, error) {
	if g.Dry || g.Yes || !g.Interactive {
		return true, nil
	}
	return g.ask(scope, func(p prompt.Prompter) (bool, error) {
		return p.Confirm(ctx, question)
	})
}

func (g Gate) Consent(ctx context.Context, scope Scope, shown *planv1.ChangePlan, question string) (bool, error) {
	return g.consent(scope, shown, func(p prompt.Prompter) (bool, error) {
		return p.Confirm(ctx, question)
	})
}

func (g Gate) ConsentByName(ctx context.Context, scope Scope, shown *planv1.ChangePlan, label, name string) (bool, error) {
	return g.consent(scope, shown, func(p prompt.Prompter) (bool, error) {
		return p.Phrase(ctx, label, name)
	})
}

func (g Gate) consent(scope Scope, shown *planv1.ChangePlan, ask func(prompt.Prompter) (bool, error)) (bool, error) {
	if nothingToChange(shown) || g.Yes {
		return true, nil
	}
	if !g.Interactive {
		return false, g.blocked()
	}
	return g.ask(scope, ask)
}

func nothingToChange(shown *planv1.ChangePlan) bool {
	return len(shown.GetGroups()) > 0 && !Mutates(shown)
}

func (g Gate) ask(scope Scope, ask func(prompt.Prompter) (bool, error)) (bool, error) {
	resume := scope.Hold(&streamv1.WaitingEvent{})
	granted, err := ask(prompt.New(g.Out, g.In))
	resume("answered")
	if err != nil || granted {
		return granted, err
	}
	scope.Say("Not confirmed, so this run changes nothing")
	return false, nil
}

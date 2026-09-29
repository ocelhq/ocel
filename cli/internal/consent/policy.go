package consent

import (
	"context"
	"fmt"
	"io"

	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	planv1 "github.com/ocelhq/ocel/pkg/proto/common/plan/v1"
)

type Policy struct {
	Command          string
	ConfirmsPlan     bool
	Yes              bool
	DryRun           bool
	Interactive      bool
	UnattendedRemedy string
	In               io.Reader
	Out              io.Writer
}

func NewPolicy(command string, yes, interactive bool, out io.Writer, in io.Reader) Policy {
	return Policy{Command: command, Yes: yes, Interactive: interactive, In: in, Out: out}
}

func NewPlanPolicy(command string, yes, interactive bool, out io.Writer, in io.Reader) Policy {
	policy := NewPolicy(command, yes, interactive, out, in)
	policy.ConfirmsPlan = true
	return policy
}

func (p Policy) IsAsking() bool { return p.Interactive && !p.Yes }

func (p Policy) Refuse() error {
	if !p.ConfirmsPlan || p.DryRun || p.Yes || p.Interactive {
		return nil
	}
	return p.unattended()
}

func (p Policy) unattended() error {
	remedy := p.UnattendedRemedy
	if remedy == "" {
		remedy = "pass --yes"
	}
	return fmt.Errorf("`%s` needs a terminal to confirm the plan it shows before applying it; to run it unattended, %s", p.Command, remedy)
}

func (p Policy) Confirm(ctx context.Context, span *run.Span, question string) (bool, error) {
	if p.DryRun || p.Yes || !p.Interactive {
		return true, nil
	}
	return p.ask(span, func(prompt terminal.Prompt) (bool, error) {
		return prompt.Confirm(ctx, question)
	})
}

func (p Policy) ConfirmPlan(ctx context.Context, span *run.Span, shown *planv1.ChangePlan, question string) (bool, error) {
	return p.confirmPlan(span, shown, func(prompt terminal.Prompt) (bool, error) {
		return prompt.Confirm(ctx, question)
	})
}

func (p Policy) ConfirmPlanByName(ctx context.Context, span *run.Span, shown *planv1.ChangePlan, label, name string) (bool, error) {
	return p.confirmPlan(span, shown, func(prompt terminal.Prompt) (bool, error) {
		return prompt.Phrase(ctx, label, name)
	})
}

func (p Policy) confirmPlan(span *run.Span, shown *planv1.ChangePlan, ask func(terminal.Prompt) (bool, error)) (bool, error) {
	if nothingToChange(shown) || p.Yes {
		return true, nil
	}
	if !p.Interactive {
		return false, p.unattended()
	}
	return p.ask(span, ask)
}

func nothingToChange(shown *planv1.ChangePlan) bool {
	return len(shown.GetGroups()) > 0 && !Mutates(shown)
}

func (p Policy) ask(span *run.Span, ask func(terminal.Prompt) (bool, error)) (bool, error) {
	granted, err := span.Confirm(func() (bool, error) {
		return ask(terminal.NewPrompt(p.Out, p.In))
	})
	if err != nil || granted {
		return granted, err
	}
	span.Say("Not confirmed, so this run changes nothing")
	return false, nil
}

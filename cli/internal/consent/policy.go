package consent

import (
	"context"
	"fmt"
	"io"

	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/jobinput"
	"github.com/ocelhq/ocel/cli/internal/prompt"
	"github.com/ocelhq/ocel/cli/internal/run"
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
	Job              *jobinput.Answers
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

func (p Policy) RefuseQuestion(question, before string) error {
	if p.Yes || p.Interactive || p.Job != nil {
		return nil
	}
	return p.refuseUnattended(fmt.Sprintf("ask %q %s", question, before))
}

func (p Policy) unattended() error {
	return p.refuseUnattended("confirm the plan it shows before applying it")
}

func (p Policy) refuseUnattended(need string) error {
	remedy := p.UnattendedRemedy
	if remedy == "" {
		remedy = "pass --yes"
	}
	return clierror.NewConfirmationRequired(
		fmt.Errorf("`%s` needs a terminal, without --json, to %s; to run it unattended, %s", p.Command, need, remedy),
		"--yes",
	)
}

func (p Policy) Confirm(ctx context.Context, span *run.Span, guard Guard) (bool, error) {
	if p.DryRun || p.Yes {
		return true, nil
	}
	if p.Job != nil {
		granted, err := p.Job.Confirm(ctx, span, guard.Finding, guard.Question)
		return sayIfDeclined(span, granted, err)
	}
	if !p.Interactive {
		span.Assume(string(guard.ID), guard.assumption())
		return true, nil
	}
	return p.ask(span, func(user prompt.Prompt) (bool, error) {
		return user.Confirm(ctx, guard.Question)
	})
}

func (p Policy) Offer(ctx context.Context, span *run.Span, question string) (bool, error) {
	if p.Yes {
		return true, nil
	}
	return span.Confirm(func() (bool, error) {
		return prompt.New(p.Out, p.In).Confirm(ctx, question)
	})
}

func (p Policy) ConfirmPlan(ctx context.Context, span *run.Span, shown *planv1.ChangePlan, question string) (bool, error) {
	return p.confirmPlan(span, shown, func(user prompt.Prompt) (bool, error) {
		return user.Confirm(ctx, question)
	})
}

func (p Policy) ConfirmPlanByName(ctx context.Context, span *run.Span, shown *planv1.ChangePlan, label, name string) (bool, error) {
	return p.confirmPlan(span, shown, func(user prompt.Prompt) (bool, error) {
		return user.Phrase(ctx, label, name)
	})
}

func (p Policy) confirmPlan(span *run.Span, shown *planv1.ChangePlan, ask func(prompt.Prompt) (bool, error)) (bool, error) {
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

func (p Policy) ask(span *run.Span, ask func(prompt.Prompt) (bool, error)) (bool, error) {
	granted, err := span.Confirm(func() (bool, error) {
		return ask(prompt.New(p.Out, p.In))
	})
	return sayIfDeclined(span, granted, err)
}

func sayIfDeclined(span *run.Span, granted bool, err error) (bool, error) {
	if err != nil || granted {
		return granted, err
	}
	span.Say("Not confirmed; nothing changed")
	return false, nil
}

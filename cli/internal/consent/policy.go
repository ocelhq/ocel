package consent

import (
	"context"
	"fmt"
	"io"

	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/confirmation"
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
	Respondent       confirmation.Respondent
}

func NewPolicy(command string, yes, interactive bool, out io.Writer, in io.Reader) Policy {
	return Policy{Command: command, Yes: yes, Interactive: interactive, In: in, Out: out}
}

func (p Policy) IsAsking() bool { return p.Interactive && !p.Yes }

func (p Policy) respondent() confirmation.Respondent {
	if p.Respondent == nil {
		return confirmation.Nobody{}
	}
	return p.Respondent
}

func (p Policy) Refuse() error {
	if !p.ConfirmsPlan || p.DryRun || p.Yes || p.respondent().CanAnswer() {
		return nil
	}
	return p.unattended()
}

func (p Policy) RefuseQuestion(question, before string) error {
	if p.Yes || p.respondent().CanAnswer() {
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
	respondent := p.respondent()
	if !respondent.CanAnswer() {
		span.Assume(string(guard.ID), guard.assumption())
		return true, nil
	}
	granted, err := respondent.Confirm(ctx, span, confirmation.Question{Finding: guard.Finding, FindingShown: true, Text: guard.Question})
	return sayIfDeclined(span, granted, err)
}

func (p Policy) Offer(ctx context.Context, span *run.Span, question string) (bool, error) {
	if p.Yes {
		return true, nil
	}
	return p.respondent().Confirm(ctx, span, confirmation.Question{Text: question})
}

func (p Policy) ConfirmPlan(ctx context.Context, span *run.Span, shown *planv1.ChangePlan, question string) (bool, error) {
	return p.confirmPlan(ctx, span, shown, confirmation.Question{Text: question})
}

func (p Policy) ConfirmPlanByName(ctx context.Context, span *run.Span, shown *planv1.ChangePlan, label, name string) (bool, error) {
	return p.confirmPlan(ctx, span, shown, confirmation.Question{Text: fmt.Sprintf("Apply this plan to %s?", name), Label: label, Phrase: name})
}

func (p Policy) confirmPlan(ctx context.Context, span *run.Span, shown *planv1.ChangePlan, question confirmation.Question) (bool, error) {
	if nothingToChange(shown) || p.Yes {
		return true, nil
	}
	respondent := p.respondent()
	if !respondent.CanAnswer() {
		return false, p.unattended()
	}
	question.Finding, question.FindingShown = terminal.PlanText(shown), true
	granted, err := respondent.Confirm(ctx, span, question)
	return sayIfDeclined(span, granted, err)
}

func nothingToChange(shown *planv1.ChangePlan) bool {
	return len(shown.GetGroups()) > 0 && !Mutates(shown)
}

func sayIfDeclined(span *run.Span, granted bool, err error) (bool, error) {
	if err != nil || granted {
		return granted, err
	}
	span.Say("Not confirmed, so this run changes nothing")
	return false, nil
}

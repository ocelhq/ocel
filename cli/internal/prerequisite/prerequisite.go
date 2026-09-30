package prerequisite

import (
	"context"
	"errors"

	"github.com/ocelhq/ocel/cli/internal/consent"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/terminal"
)

type Kind int

const (
	Project Kind = iota + 1
	Bootstrap
	Domain
)

type MissingError interface {
	error
	Missing() Kind
	Finding() string
	Remedy() string
}

type Setup struct {
	Prompt         func(missing MissingError) string
	ChangesAccount bool
	Run            func(ctx context.Context, policy consent.Policy, span *run.Span, missing MissingError) error
}

type Setups map[Kind]Setup

type SetupDeclinedError struct{ Missing MissingError }

func (e SetupDeclinedError) Error() string {
	return "Not set up, so this run changes nothing. When you're ready: " + e.Missing.Remedy()
}

func IsDeclined(err error) bool {
	var declined SetupDeclinedError
	return errors.As(err, &declined)
}

func (s Setups) Ensure(ctx context.Context, policy consent.Policy, span *run.Span, check func(context.Context) error) error {
	set := map[Kind]bool{}
	for {
		err := check(ctx)
		var missing MissingError
		if !errors.As(err, &missing) || !policy.Interactive || set[missing.Missing()] {
			return err
		}
		setup, ok := s[missing.Missing()]
		if !ok || (setup.ChangesAccount && policy.DryRun) {
			return err
		}
		set[missing.Missing()] = true
		span.Say(missing.Finding())
		if setup.Prompt != nil {
			granted, err := confirm(ctx, policy, span, setup.Prompt(missing))
			if err != nil {
				return err
			}
			if !granted {
				return SetupDeclinedError{Missing: missing}
			}
		}
		if err := setup.Run(ctx, policy, span, missing); err != nil {
			return err
		}
	}
}

func confirm(ctx context.Context, policy consent.Policy, span *run.Span, question string) (bool, error) {
	if policy.Yes {
		return true, nil
	}
	return span.Confirm(func() (bool, error) {
		return terminal.NewPrompt(policy.Out, policy.In).Confirm(ctx, question)
	})
}

package prerequisite

import (
	"context"
	"errors"

	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/consent"
	"github.com/ocelhq/ocel/cli/internal/run"
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
	Hint() string
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
		if !errors.As(err, &missing) {
			return err
		}
		if !policy.Interactive || set[missing.Missing()] {
			return refuseMissing(err, missing)
		}
		setup, ok := s[missing.Missing()]
		if !ok || (setup.ChangesAccount && policy.DryRun) {
			return refuseMissing(err, missing)
		}
		set[missing.Missing()] = true
		span.Say(missing.Finding())
		if setup.Prompt != nil {
			granted, offerErr := policy.Offer(ctx, span, setup.Prompt(missing))
			if offerErr != nil {
				return offerErr
			}
			if !granted {
				return refuseDeclined(err, missing)
			}
		}
		if err := setup.Run(ctx, policy, span, missing); err != nil {
			return err
		}
	}
}

func refuseMissing(found error, missing MissingError) error {
	if errors.As(found, new(*clierror.Error)) {
		return found
	}
	return &clierror.Error{Code: clierror.CodePrerequisiteMissing, Message: missing.Finding(), Hint: missing.Hint(), Cause: found}
}

func refuseDeclined(found error, missing MissingError) error {
	declined := SetupDeclinedError{Missing: missing}
	var coded *clierror.Error
	if !errors.As(found, &coded) {
		return &clierror.Error{Code: clierror.CodePrerequisiteMissing, Message: missing.Finding(), Hint: missing.Hint(), Cause: declined}
	}
	refusal := *coded
	refusal.Cause = declined
	return &refusal
}

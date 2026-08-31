package providerkit

import (
	"context"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

type StandingVerdict int

const (
	StandingPass StandingVerdict = iota
	StandingOwed
	StandingFail
)

type StandingCheck struct {
	Subject string
	Verdict StandingVerdict
	Finding string
	Fix     string
}

type StandingRequest struct {
	Class     Class
	Hostnames []string
}

type StandingChecker interface {
	CheckStanding(ctx context.Context, req StandingRequest) ([]StandingCheck, error)
}

func StandingProto(checks []StandingCheck) []*contractv1.StandingCheck {
	if len(checks) == 0 {
		return nil
	}
	wired := make([]*contractv1.StandingCheck, 0, len(checks))
	for _, check := range checks {
		wired = append(wired, &contractv1.StandingCheck{
			Subject: check.Subject,
			Verdict: verdictProto(check.Verdict),
			Finding: check.Finding,
			Fix:     check.Fix,
		})
	}
	return wired
}

func verdictProto(verdict StandingVerdict) contractv1.StandingCheck_Verdict {
	switch verdict {
	case StandingOwed:
		return contractv1.StandingCheck_VERDICT_OWED
	case StandingFail:
		return contractv1.StandingCheck_VERDICT_FAIL
	default:
		return contractv1.StandingCheck_VERDICT_PASS
	}
}

func (h *handlers) standingChecks(ctx context.Context, provider Provider, class Class, hostnames []string) ([]*contractv1.StandingCheck, error) {
	checker, held := provider.(StandingChecker)
	if !held {
		return nil, nil
	}
	checks, err := checker.CheckStanding(ctx, StandingRequest{Class: class, Hostnames: hostnames})
	if err != nil {
		return nil, err
	}
	return StandingProto(checks), nil
}

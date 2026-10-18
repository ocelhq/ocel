package providerserver

import (
	"context"

	"github.com/ocelhq/ocel/pkg/progress"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func consentedPlanOf(req *contractv1.DeployRequest) (*provider.Plan, error) {
	if req.GetConsented() == nil {
		return nil, nil
	}
	shown, err := PlanFromProto(req.GetConsented())
	if err != nil {
		return nil, err
	}
	return &shown, nil
}

func refuseConsentedDryDeploy() error {
	return refusal.Refuse(refusal.CodeInvalid, "a dry deploy applies nothing, so there is nothing for a consented plan to bound")
}

func (a applied) refuseUnconsented(ctx context.Context, r *deployRun, stack provider.StackSpec, progress progress.Log, minted *provider.AppEntry) error {
	if a.consented == nil {
		return nil
	}
	fresh, err := r.provider.Stacks().Plan(ctx, stack, progress)
	if err != nil {
		return err
	}
	if minted != nil {
		fresh = withReleaseMintedAtDeploy(fresh, minted.Release.Token())
	}
	return provider.RefuseUnconsentedChanges(*a.consented, fresh)
}

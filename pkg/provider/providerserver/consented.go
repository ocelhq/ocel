package providerserver

import (
	"context"

	"github.com/ocelhq/ocel/pkg/progress"
	planv1 "github.com/ocelhq/ocel/pkg/proto/common/plan/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func consentedPlanOf(consented *planv1.ChangePlan) (*provider.Plan, error) {
	if consented == nil {
		return nil, nil
	}
	shown, err := PlanFromProto(consented)
	if err != nil {
		return nil, err
	}
	return &shown, nil
}

func refuseConsentedDryDeploy() error {
	return refusal.Refuse(refusal.CodeInvalid, "a dry deploy applies nothing, so there is nothing for a consented plan to bound")
}

func (a applied) refuseUnconsented(fresh func() (provider.Plan, error)) error {
	if a.consented == nil {
		return nil
	}
	drawn, err := fresh()
	if err != nil {
		return err
	}
	return provider.RefuseUnconsentedChanges(*a.consented, drawn)
}

func (a applied) refuseUnconsentedStack(ctx context.Context, r *deployRun, stack provider.StackSpec, progress progress.Log, minted *provider.AppEntry) error {
	return a.refuseUnconsented(func() (provider.Plan, error) {
		fresh, err := r.provider.Stacks().Plan(ctx, stack, progress)
		if err != nil || minted == nil {
			return fresh, err
		}
		return withReleaseMintedAtDeploy(fresh, minted.Release.Token()), nil
	})
}

func (r *deployRun) refuseUnconsentedDeploy(ctx context.Context) error {
	return r.mode.refuseUnconsented(func() (provider.Plan, error) {
		var fresh provider.Plan
		preflight, err := r.planPreflight(ctx)
		if err != nil {
			return provider.Plan{}, err
		}
		fresh.Groups = append(fresh.Groups, preflight.Groups...)
		if !r.infraCurrent {
			infra, err := r.planInfra(ctx, nil)
			if err != nil {
				return provider.Plan{}, err
			}
			fresh.Groups = append(fresh.Groups, infra.Groups...)
		}
		fresh.Groups = append(fresh.Groups, r.planEdgeGroup())
		retired, err := planRetirement(ctx, r.provider, r.liveApps(), progress.Discard())
		if err != nil {
			return provider.Plan{}, err
		}
		fresh.Groups = append(fresh.Groups, retired.Groups...)
		return fresh, nil
	})
}

func (r *deployRun) refuseUnconsentedInfra(ctx context.Context, undeclared []*contractv1.ManifestResource) error {
	if r.infraCurrent {
		return nil
	}
	return r.mode.refuseUnconsented(func() (provider.Plan, error) {
		return r.planInfra(ctx, undeclared)
	})
}

func (r *deployRun) planInfra(ctx context.Context, undeclared []*contractv1.ManifestResource) (provider.Plan, error) {
	stack, _, err := r.infraStack(ctx, undeclared)
	if err != nil {
		return provider.Plan{}, err
	}
	drawn, err := r.provider.Stacks().Plan(ctx, stack, progress.Discard())
	if err != nil {
		return provider.Plan{}, err
	}
	values, err := r.planValuesGroup(ctx)
	if err != nil {
		return provider.Plan{}, err
	}
	if len(values.Changes) > 0 {
		drawn.Groups = append(drawn.Groups, values)
	}
	return drawn, nil
}

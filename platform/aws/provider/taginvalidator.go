package aws

import (
	"context"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	"github.com/ocelhq/ocel/platform/aws/provider/taginvalidator"
)

func (p *Provider) tagInvalidatorWork(ctx context.Context, tier environment.Tier) (partWork, error) {
	deployed, err := p.bootstrapped(ctx, tier)
	if err != nil {
		return partWork{}, err
	}
	if err := p.requireBootstrapped(deployed, tier); err != nil {
		return partWork{}, err
	}
	account, err := p.accountID(ctx)
	if err != nil {
		return partWork{}, err
	}
	seed, err := bootstrap.PurgeSeedParamFor(p.namespace, tier, bootstrap.KindCloudflare)
	if err != nil {
		return partWork{}, err
	}
	spec := taginvalidator.Spec{
		Namespace:  string(p.namespace),
		Tier:       tier,
		Region:     p.aws.Region,
		Account:    account,
		Role:       deployed.TagInvalidatorRoleARN,
		StateTable: deployed.StateTable,
		StreamARN:  deployed.StateTableStreamARN,
		SeedParam:  seed,
	}
	return partWork{
		name:    taginvalidator.Part,
		summary: taginvalidator.Summary,
		digest:  taginvalidator.Digest(spec),
		shape:   taginvalidator.Shape(spec.Namespace, tier),
		make: func(ctx context.Context, _ []provider.PartResource, _ bool) ([]provider.PartResource, error) {
			if spec.Role == "" {
				return nil, refusal.Refuse(refusal.CodeNotReady, "the %s bootstrap predates the role its tag invalidator runs as, so the %s part has no role to make its function under: run `%s` and deploy again", tier, taginvalidator.Part, provider.BootstrapCommand(tier))
			}
			return taginvalidator.Ensure(ctx, taginvalidator.NewClients(p.aws), spec)
		},
	}, nil
}

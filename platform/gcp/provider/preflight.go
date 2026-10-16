package gcp

import (
	"context"
	"slices"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
)

func (p *Provider) PreflightDeploy(ctx context.Context, pre provider.DeployPreflight) error {
	var parts []string
	for _, resource := range pre.Resources {
		switch resource.Type {
		case provider.BindingBucket:
			if resource.Bucket != nil && resource.Bucket.Public {
				return refusal.Refuse(refusal.CodeInvalid,
					"bucket %s asks to be public, and this provider creates its buckets under public access prevention, which refuses any grant to allUsers: "+
						"serve the objects through your app or a signed url instead, or drop `public` from %s",
					resource.Name, resource.Name)
			}
		case provider.BindingPostgres:
			if _, err := readPostgres(resource); err != nil {
				return err
			}
			parts = appendMissing(parts, []string{networkPart})
		case provider.BindingKV:
			if _, err := readKVStore(resource); err != nil {
				return err
			}
			parts = appendMissing(parts, []string{networkPart})
		case provider.BindingTopic, provider.BindingTask:
			declared := declaredTopicOf(resource).declared
			if pre.Deploy.Infra.IsZero() {
				return refusal.Refuse(refusal.CodeUnsupported,
					"this project declares %s %s, and an ephemeral preview provisions no infra stack for its Pub/Sub topics and subscriptions to live in: "+
						"deploy it to a named environment", resource.Type, declared)
			}
			parts = appendMissing(parts, []string{tasksPart})
		}
	}
	if slices.ContainsFunc(pre.Uses, func(uses buildoutput.Uses) bool { return uses.Revalidation }) {
		parts = append(parts, tagDatabasePart)
	}
	if pre.Edge == alb.Kind {
		parts = append(parts, albPart)
	} else if err := p.leaveALB(ctx, pre); err != nil {
		return err
	}
	return p.addParts(ctx, pre, parts)
}

func (p *Provider) leaveALB(ctx context.Context, pre provider.DeployPreflight) error {
	if pre.Dry || pre.Deploy.Slug == "" {
		return nil
	}
	b, err := bootstrapGate{p}.openBootstrap(ctx)
	if err != nil {
		return err
	}
	return b.releaseALB(ctx, pre.Deploy.Tier, pre.Deploy.Slug, false, pre.Progress)
}

func (p *Provider) addParts(ctx context.Context, pre provider.DeployPreflight, names []string) error {
	if len(names) == 0 {
		return nil
	}
	b, err := bootstrapGate{p}.openBootstrap(ctx)
	if err != nil {
		return err
	}
	return b.addParts(ctx, pre, names)
}

func (b bootstrap) addParts(ctx context.Context, pre provider.DeployPreflight, names []string) error {
	tier := pre.Deploy.Tier
	for _, want := range b.listDeployedParts(tier) {
		if !slices.Contains(names, want.name) {
			continue
		}
		if !pre.Dry {
			if want.name == albPart {
				if err := b.claimALB(ctx, tier, pre.Deploy.Slug); err != nil {
					return err
				}
			}
			if err := b.ensurePart(ctx, tier, pre.Deploy.Slug, want); err != nil {
				return err
			}
			continue
		}
		group, planned, err := b.planPart(ctx, tier, want)
		if err != nil {
			return err
		}
		if planned && pre.AddToPlan != nil {
			pre.AddToPlan(group)
		}
	}
	return nil
}

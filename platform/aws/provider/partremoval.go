package aws

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/apigateway"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/aws/provider/cloudfrontpart"
	"github.com/ocelhq/ocel/platform/aws/provider/imageoptimizer"
	"github.com/ocelhq/ocel/platform/aws/provider/notfoundapi"
	"github.com/ocelhq/ocel/platform/aws/provider/taginvalidator"
)

func (p *Provider) removeAddedParts(ctx context.Context, tier environment.Tier) error {
	namespace := string(p.namespace)
	for _, name := range addedParts {
		recorded, present, err := stackrecords.ReadBootstrapPart(ctx, p.KeyValues(), tier, name)
		if err != nil {
			return err
		}
		if !present {
			continue
		}
		switch name {
		case imageoptimizer.Part:
			err = imageoptimizer.Remove(ctx, imageoptimizer.NewClients(p.aws), namespace, tier)
		case taginvalidator.Part:
			err = taginvalidator.Remove(ctx, taginvalidator.NewClients(p.aws), namespace, tier)
		case cloudfrontpart.Part:
			var api cloudfrontpart.API
			if api, err = p.cloudFrontAPI(ctx); err == nil {
				err = cloudfrontpart.Remove(ctx, api, cloudfrontpart.Spec{Namespace: p.namespace, Tier: tier}, recorded.Resources)
			}
		case notfoundapi.Part:
			err = notfoundapi.Remove(ctx, apigateway.NewFromConfig(p.aws), recorded.Resources)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (p *Provider) planAddedPartRemoval(ctx context.Context, tier environment.Tier) ([]provider.ChangeGroup, error) {
	var groups []provider.ChangeGroup
	for _, name := range addedParts {
		recorded, present, err := stackrecords.ReadBootstrapPart(ctx, p.KeyValues(), tier, name)
		if err != nil {
			return nil, err
		}
		if !present {
			continue
		}
		group := provider.ChangeGroup{
			Kind:   provider.StackGroupKind,
			Name:   "bootstrap part " + name,
			Action: provider.ActionDelete,
			Reason: "added by " + recorded.AddedBy + " on first use; the bootstrap it belongs to is being removed",
		}
		for _, resource := range recorded.Resources {
			group.Changes = append(group.Changes, provider.Change{Kind: resource.Kind, Name: resource.ID, Action: provider.ActionDelete})
		}
		groups = append(groups, group)
	}
	return groups, nil
}

package gcp

import (
	"context"
	"slices"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func (p *Provider) PreflightDeploy(ctx context.Context, pre provider.DeployPreflight) error {
	var stores []string
	for _, resource := range pre.Resources {
		if resource.Type != provider.BindingKV {
			continue
		}
		if _, err := readKVStore(resource); err != nil {
			return err
		}
		stores = append(stores, resource.Name)
	}
	if len(stores) == 0 {
		return nil
	}
	clients, err := p.openClients(ctx)
	if err != nil {
		return err
	}
	tier := pre.Deploy.Tier
	read, err := bootstrap{clients: clients}.stamped(ctx, clients.Bucket(tier))
	if err != nil {
		return err
	}
	if read.stamp.State == stateComplete && slices.Contains(read.stamp.Features, kvFeature) {
		return nil
	}
	return refusal.Refuse(refusal.CodeNotReady,
		"this project declares kv %s, and a store on gcp is reached over the %s tier's network in %s, which its bootstrap has not installed.\n"+
			"Run `%s %s`, then deploy again",
		stores[0], tier, clients.region, provider.BootstrapFeaturesCommand(tier), kvFeature)
}

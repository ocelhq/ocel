package gcp

import (
	"context"
	"slices"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

type featureNeed struct {
	feature  string
	declares string
	reason   string
}

func (p *Provider) PreflightDeploy(ctx context.Context, pre provider.DeployPreflight) error {
	var needs []featureNeed
	for _, resource := range pre.Resources {
		switch resource.Type {
		case provider.BindingKV:
			if _, err := readKVStore(resource); err != nil {
				return err
			}
			needs = append(needs, featureNeed{feature: kvFeature, declares: "kv " + resource.Name,
				reason: "a store on gcp is reached over the tier's network, which its bootstrap has not installed"})
		case provider.BindingTopic, provider.BindingTask:
			declared := declaredTopicOf(resource).declared
			if pre.Deploy.Infra.IsZero() {
				return refusal.Refuse(refusal.CodeUnsupported,
					"this project declares %s %s, and an ephemeral preview provisions no infra stack for its Pub/Sub topics and subscriptions to live in: "+
						"deploy it to a named environment", resource.Type, declared)
			}
			needs = append(needs, featureNeed{feature: tasksFeature, declares: string(resource.Type) + " " + declared,
				reason: "topics and tasks on gcp keep their runs in the tier's task database and wait in its delay queue, which its bootstrap has not installed"})
		}
	}
	if len(needs) == 0 {
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
	for _, need := range needs {
		if read.stamp.State == stateComplete && slices.Contains(read.stamp.Features, need.feature) {
			continue
		}
		return refuseUninstalled(tier, clients.region, need)
	}
	return nil
}

func refuseUninstalled(tier environment.Tier, region string, need featureNeed) error {
	return refusal.Refuse(refusal.CodeNotReady,
		"this project declares %s, and %s in %s.\nRun `%s %s`, then deploy again",
		need.declares, need.reason, region, provider.BootstrapFeaturesCommand(tier), need.feature)
}

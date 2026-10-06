package gcp

import (
	"context"
	"slices"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/edge"
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
	proxied, err := p.proxiesPreviews(pre)
	if err != nil {
		return err
	}
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
	var facts edge.Facts
	opened := false
	for _, entry := range pre.Deploy.Apps {
		framework, compute := entry.Manifest.GetFramework().GetName(), entry.Compute()
		if framework != buildoutput.FrameworkNext || compute != provider.ComputeServerless {
			continue
		}
		if !opened {
			front, err := p.Edges().Open(pre.Edge, nil)
			if err != nil {
				return err
			}
			facts, opened = front.Facts(), true
		}
		if refreshesByTask(framework, compute, facts, proxied) {
			needs = append(needs, featureNeed{feature: tasksFeature, declares: "Next app " + entry.App,
				reason: "a Next app billed per request on Cloud Run refreshes a stale page through the tier's Cloud Tasks queue, which its bootstrap has not installed"})
		}
	}
	if len(needs) == 0 && !proxied {
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
	if proxied {
		if err := refuseMissingProxyAgent(tier, read.stamp); err != nil {
			return err
		}
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

func (p *Provider) proxiesPreviews(pre provider.DeployPreflight) (bool, error) {
	if pre.Deploy.Tier != environment.TierPreview || p.emulated() {
		return false, nil
	}
	front, err := p.Edges().Open(pre.Edge, nil)
	if err != nil {
		return false, err
	}
	return p.gatesBehindIAP(pre.Deploy.Tier, factsOf(front)), nil
}

func refuseMissingProxyAgent(tier environment.Tier, written stamp) error {
	if written.hasAgent(proxyAPI) {
		return nil
	}
	return refusal.Refuse(refusal.CodeNotReady,
		"this preview has no edge in front, so it is served behind Identity-Aware Proxy, and no bootstrap of tier %s has checked %s is on "+
			"and created the proxy's service agent.\nRun `%s`, then deploy again",
		tier, proxyAPI, provider.BootstrapCommand(tier))
}

package aws

import (
	"context"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/lambda"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/aws/provider/account"
	"github.com/ocelhq/ocel/platform/aws/provider/control"
)

func (p *Provider) PlanPreflight(ctx context.Context, pre provider.DeployPreflight) (provider.Plan, error) {
	if err := p.checkDeploy(ctx, pre); err != nil {
		return provider.Plan{}, err
	}
	if err := control.ResolveRuntimeLayers(ctx, p.Provider, pre, func(account.APIs, account.Deployed) (map[string]string, error) {
		return account.ReadRuntimeLayersToPlan(ctx, lambda.NewFromConfig(p.Config), p.Namespace, pre.Deploy.Tier)
	}); err != nil {
		return provider.Plan{}, err
	}
	if err := p.stacks.Preflight(ctx, pre); err != nil {
		return provider.Plan{}, err
	}
	groups, err := p.planDeployParts(ctx, pre)
	return provider.Plan{Groups: groups}, err
}

func (p *Provider) ApplyPreflight(ctx context.Context, pre provider.DeployPreflight) error {
	if err := p.checkDeploy(ctx, pre); err != nil {
		return err
	}
	if err := control.ResolveRuntimeLayers(ctx, p.Provider, pre, func(apis account.APIs, deployed account.Deployed) (map[string]string, error) {
		return account.EnsureRuntimeLayers(ctx, apis, p.Namespace, pre.Deploy.Tier,
			account.RuntimeLayerRequest{ArtifactBucket: deployed.Buckets[naming.StorageArtifacts], StateTable: deployed.StateTable}, pre.Progress)
	}); err != nil {
		return err
	}
	if err := p.ensureDeployParts(ctx, pre); err != nil {
		return err
	}
	return p.stacks.Preflight(ctx, pre)
}

func (p *Provider) ensureDeployParts(ctx context.Context, pre provider.DeployPreflight) error {
	tier := pre.Deploy.Tier
	deployed, err := p.ReadDeployed(ctx, tier)
	if err != nil || !deployed.Present {
		return err
	}
	records := p.PartRecords()
	used := p.tierParts(records).AddedFor(pre.Uses, pre.Edge)
	var say func(string)
	if pre.Progress != nil {
		say = pre.Progress.Say
	}
	for _, part := range used {
		if _, err := part.Added.Ensure(ctx, tier, pre.Deploy.Slug, "", say); err != nil {
			return err
		}
	}
	return stackrecords.RecordPartsUsed(ctx, records.KeyValues, tier, pre.Deploy.Slug, used.Names())
}

func (p *Provider) checkDeploy(ctx context.Context, pre provider.DeployPreflight) error {
	if err := refusePublicBuckets(pre); err != nil {
		return err
	}
	if err := refuseContainersScaledToZero(pre); err != nil {
		return err
	}
	params, err := p.ReadTierParams(ctx, pre.Deploy.Tier, pre.Edge)
	if err != nil {
		return err
	}
	return checkTierParams(pre, params, time.Now())
}

func checkTierParams(pre provider.DeployPreflight, params account.TierParams, now time.Time) error {
	nagStaleEdgeKey(pre, params, now)
	if params.OriginSecretErr != nil {
		return params.OriginSecretErr
	}
	nagStaleOriginSecret(pre, params, now)
	return nil
}

func nagStaleEdgeKey(pre provider.DeployPreflight, params account.TierParams, now time.Time) {
	if pre.Edge == "" || pre.Progress == nil || params.EdgeCredentialsErr != nil {
		return
	}
	if notice := account.StaleEdgeKeyNotice(params.EdgeCredentials, now, pre.Deploy.Tier); notice != "" {
		pre.Progress.Warn(notice)
	}
}

func nagStaleOriginSecret(pre provider.DeployPreflight, params account.TierParams, now time.Time) {
	if pre.Progress == nil {
		return
	}
	if notice := account.StaleOriginSecretNotice(params.OriginSecret, now, pre.Deploy.Tier); notice != "" {
		pre.Progress.Warn(notice)
	}
}

func refusePublicBuckets(pre provider.DeployPreflight) error {
	for _, resource := range pre.Resources {
		if resource.Bucket == nil || !resource.Bucket.Public {
			continue
		}
		return refusal.Refuse(refusal.CodeInvalid,
			"bucket %s asks to be public, and this provider provisions its buckets with public access blocked at the account's edge: serve the objects through your app or a signed url instead, or drop `public` from %s",
			resource.Name, resource.Name)
	}
	return nil
}

func refuseContainersScaledToZero(pre provider.DeployPreflight) error {
	for _, app := range pre.Deploy.Apps {
		if app.Compute() != provider.ComputeContainer || app.Instances.Min > 0 || provider.IsWorkerApp(app.App, app.Workers) {
			continue
		}
		return refusal.Refuse(refusal.CodeInvalid,
			"app %s asks for instances.min 0, and a container app on this provider scales on the requests its load balancer counts per task, which a service with no task never receives: give %s instances.min 1 or more",
			app.App, app.App)
	}
	return nil
}

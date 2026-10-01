package aws

import (
	"context"
	"maps"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
)

func (p *Provider) PreflightDeploy(ctx context.Context, pre provider.DeployPreflight) error {
	if err := refusePublicBuckets(pre); err != nil {
		return err
	}
	if err := refuseContainersScaledToZero(pre); err != nil {
		return err
	}
	if err := refuseKVOnServerlessApps(pre); err != nil {
		return err
	}
	if err := p.nagStaleEdgeKey(ctx, pre); err != nil {
		return err
	}
	if err := p.refuseUnreadableOriginSecret(ctx, pre); err != nil {
		return err
	}
	if err := p.nagStaleOriginSecret(ctx, pre); err != nil {
		return err
	}
	if err := p.publishRuntimeLayers(ctx, pre); err != nil {
		return err
	}
	return p.stacks.Preflight(ctx, pre)
}

func (p *Provider) nagStaleEdgeKey(ctx context.Context, pre provider.DeployPreflight) error {
	if pre.Edge == "" || pre.Progress == nil {
		return nil
	}
	params, err := p.tierParams(ctx, pre.Deploy.Tier, pre.Edge)
	if err != nil {
		return err
	}
	if params.EdgeCredentialsErr != nil {
		return nil
	}
	if notice := bootstrap.StaleEdgeKeyNotice(params.EdgeCredentials, time.Now(), pre.Deploy.Tier); notice != "" {
		pre.Progress.Warn(notice)
	}
	return nil
}

func (p *Provider) refuseUnreadableOriginSecret(ctx context.Context, pre provider.DeployPreflight) error {
	params, err := p.tierParams(ctx, pre.Deploy.Tier, pre.Edge)
	if err != nil {
		return err
	}
	return params.OriginSecretErr
}

func (p *Provider) nagStaleOriginSecret(ctx context.Context, pre provider.DeployPreflight) error {
	if pre.Progress == nil {
		return nil
	}
	params, err := p.tierParams(ctx, pre.Deploy.Tier, pre.Edge)
	if err != nil {
		return err
	}
	if notice := bootstrap.StaleOriginSecretNotice(params.OriginSecret, time.Now(), pre.Deploy.Tier); notice != "" {
		pre.Progress.Warn(notice)
	}
	return nil
}

func (p *Provider) publishRuntimeLayers(ctx context.Context, pre provider.DeployPreflight) error {
	if pre.Dry {
		return nil
	}
	tier := pre.Deploy.Tier
	deployed, err := p.bootstrapped(ctx, tier)
	if err != nil || !deployed.Present {
		return err
	}
	published, err := bootstrap.EnsureRuntimeLayers(ctx, bootstrap.APIs{
		CFN:   cloudformation.NewFromConfig(p.aws),
		Store: s3.NewFromConfig(p.aws),
	}, p.namespace, tier, bootstrap.RuntimeLayerRequest{
		ArtifactBucket: deployed.ArtifactBucket,
		Writer:         pre.WrittenBy,
	}, pre.Progress)
	if err != nil {
		return err
	}
	if !maps.Equal(published, deployed.RuntimeLayers) {
		p.deployed.forget()
	}
	return nil
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

func refuseKVOnServerlessApps(pre provider.DeployPreflight) error {
	serverless := map[string]bool{}
	for _, app := range pre.Deploy.Apps {
		serverless[app.App] = app.Compute() != provider.ComputeContainer
	}
	for _, usage := range pre.Apps {
		if !serverless[usage.App] {
			continue
		}
		for _, resource := range usage.Resources {
			if resource.Type != provider.BindingKV {
				continue
			}
			return refusal.Refuse(refusal.CodeUnsupported,
				"app %s uses kv %s, and this provider runs %s as functions outside the VPC the store answers in, so it could never reach it: run %s as a container app, or wait for https://github.com/ocelhq/ocel/issues/1473 (#1473), which decides how functions join the VPC",
				usage.App, resource.Name, usage.App, usage.App)
		}
	}
	return nil
}

func refuseContainersScaledToZero(pre provider.DeployPreflight) error {
	for _, app := range pre.Deploy.Apps {
		if app.Compute() != provider.ComputeContainer || app.Instances.Min > 0 {
			continue
		}
		return refusal.Refuse(refusal.CodeInvalid,
			"app %s asks for minInstances 0, and a container app on this provider scales on the requests its load balancer counts per task, which a service with no task never receives: give %s minInstances 1 or more",
			app.App, app.App)
	}
	return nil
}

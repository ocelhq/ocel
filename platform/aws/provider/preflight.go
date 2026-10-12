package aws

import (
	"context"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
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
	if err := refuseTasksWhereNothingRunsThem(pre); err != nil {
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
	if err := p.resolveRuntimeLayers(ctx, pre); err != nil {
		return err
	}
	if err := p.ensureDeployParts(ctx, pre); err != nil {
		return err
	}
	return p.stacks.Preflight(ctx, pre)
}

func (p *Provider) ensureDeployParts(ctx context.Context, pre provider.DeployPreflight) error {
	tier := pre.Deploy.Tier
	deployed, err := p.bootstrapped(ctx, tier)
	if err != nil || !deployed.Present || pre.Dry {
		return err
	}
	used, err := p.partsUsedBy(pre.Deploy.Apps, pre.Edge)
	if err != nil {
		return err
	}
	var say func(string)
	if pre.Progress != nil {
		say = pre.Progress.Say
	}
	for _, name := range used {
		work, err := p.partWorkFor(ctx, tier, name)
		if err != nil {
			return err
		}
		if _, err := p.ensurePart(ctx, tier, work, pre.Deploy.Slug, "", say); err != nil {
			return err
		}
	}
	return stackrecords.RecordPartsUsed(ctx, p.KeyValues(), tier, pre.Deploy.Slug, used)
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

func (p *Provider) resolveRuntimeLayers(ctx context.Context, pre provider.DeployPreflight) error {
	tier := pre.Deploy.Tier
	deployed, err := p.bootstrapped(ctx, tier)
	if err != nil || !deployed.Present {
		return err
	}
	p.layers.forget()
	_, err = p.layers.resolve(tier, func() (map[string]string, error) {
		if pre.Dry {
			return bootstrap.ReadRuntimeLayersToPlan(ctx, lambda.NewFromConfig(p.aws), p.namespace, tier)
		}
		return bootstrap.EnsureRuntimeLayers(ctx, bootstrap.APIs{
			Layers:  lambda.NewFromConfig(p.aws),
			Store:   s3.NewFromConfig(p.aws),
			Records: bootstrap.DynamoRecords(dynamodb.NewFromConfig(p.aws)),
		}, p.namespace, tier, bootstrap.RuntimeLayerRequest{ArtifactBucket: deployed.ArtifactBucket, StateTable: deployed.StateTable}, pre.Progress)
	})
	return err
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

func isTopicOrTask(kind provider.BindingType) bool {
	return kind == provider.BindingTask || kind == provider.BindingTopic
}

func refuseTasksWhereNothingRunsThem(pre provider.DeployPreflight) error {
	if pre.Deploy.Infra.IsZero() {
		for _, resource := range pre.Resources {
			if isTopicOrTask(resource.Type) && resource.Binding == "" {
				return refusal.Refuse(refusal.CodeUnsupported,
					"this ephemeral preview declares %s %s, and an ephemeral preview on this provider provisions no infrastructure of its own, so no queue or worker would run it: deploy it to a persistent environment",
					resource.Type, resource.Declared)
			}
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
			"app %s asks for instances.min 0, and a container app on this provider scales on the requests its load balancer counts per task, which a service with no task never receives: give %s instances.min 1 or more",
			app.App, app.App)
	}
	return nil
}

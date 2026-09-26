package aws

import (
	"context"
	"maps"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	"github.com/ocelhq/ocel/platform/aws/provider/edges"
)

func (p *Provider) PreflightDeploy(ctx context.Context, pre providerkit.DeployPreflight) error {
	if err := refuseContainersBehindFunctionEdge(pre); err != nil {
		return err
	}
	if err := refusePublicBuckets(pre); err != nil {
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

func (p *Provider) nagStaleEdgeKey(ctx context.Context, pre providerkit.DeployPreflight) error {
	if pre.Edge == "" || pre.Progress == nil {
		return nil
	}
	params, err := p.classParams(ctx, pre.Plan.Class, pre.Edge)
	if err != nil {
		return err
	}
	if params.EdgeCredentialsErr != nil {
		return nil
	}
	if notice := bootstrap.StaleEdgeKeyNotice(params.EdgeCredentials, time.Now(), string(pre.Plan.Class)); notice != "" {
		pre.Progress.Detail(notice)
	}
	return nil
}

func (p *Provider) refuseUnreadableOriginSecret(ctx context.Context, pre providerkit.DeployPreflight) error {
	params, err := p.classParams(ctx, pre.Plan.Class, pre.Edge)
	if err != nil {
		return err
	}
	return params.OriginSecretErr
}

func (p *Provider) nagStaleOriginSecret(ctx context.Context, pre providerkit.DeployPreflight) error {
	if pre.Progress == nil {
		return nil
	}
	params, err := p.classParams(ctx, pre.Plan.Class, pre.Edge)
	if err != nil {
		return err
	}
	if notice := bootstrap.StaleOriginSecretNotice(params.OriginSecret, time.Now(), string(pre.Plan.Class)); notice != "" {
		pre.Progress.Detail(notice)
	}
	return nil
}

func (p *Provider) publishRuntimeLayers(ctx context.Context, pre providerkit.DeployPreflight) error {
	if pre.Dry {
		return nil
	}
	class := pre.Plan.Class
	held, err := p.bootstrapped(ctx, class)
	if err != nil || !held.Present {
		return err
	}
	published, err := bootstrap.EnsureRuntimeLayers(ctx, bootstrap.APIs{
		CFN:   cloudformation.NewFromConfig(p.aws),
		Store: s3.NewFromConfig(p.aws),
	}, p.namespace, string(class), bootstrap.RuntimeLayerRequest{
		ArtifactBucket: held.ArtifactBucket,
		Writer:         pre.WrittenBy,
	}, saying(pre.Progress))
	if err != nil {
		return err
	}
	if !maps.Equal(published, held.RuntimeLayers) {
		p.deployed.forget()
	}
	return nil
}

func saying(progress providerkit.Progress) func(string) {
	if progress == nil {
		return nil
	}
	return progress.Say
}

func refuseContainersBehindFunctionEdge(pre providerkit.DeployPreflight) error {
	if pre.Edge == edges.DefaultKind {
		return nil
	}
	for _, app := range pre.Plan.Apps {
		if app.Compute() != providerkit.ComputeContainer {
			continue
		}
		return providerkit.Refuse(providerkit.CodeInvalid,
			"app %s runs as a container, and the %q edge reaches a release's entry function rather than an origin that demands the class's secret, so it has no way to reach one: front this project with %q, or give %s `compute: \"serverless\"`",
			app.App, pre.Edge, edges.DefaultKind, app.App)
	}
	return nil
}

func refusePublicBuckets(pre providerkit.DeployPreflight) error {
	for _, resource := range pre.Resources {
		if resource.Bucket == nil || !resource.Bucket.Public {
			continue
		}
		return providerkit.Refuse(providerkit.CodeInvalid,
			"bucket %s asks to be public, and this provider stands its buckets up with public access blocked at the account's edge: serve the objects through your app or a signed url instead, or drop `public` from %s",
			resource.Name, resource.Name)
	}
	return nil
}

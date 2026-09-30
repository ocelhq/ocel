package aws

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
)

func (p *Provider) PreflightDeploy(ctx context.Context, pre provider.DeployPreflight) error {
	if err := refuseUnpairedApps(p.Facts(), pre); err != nil {
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

func refuseUnpairedApps(facts provider.Facts, pre provider.DeployPreflight) error {
	for _, app := range pre.Deploy.Apps {
		compute := app.Compute()
		if _, paired := facts.PairedRouter(pre.Edge, compute); paired {
			continue
		}
		return refusal.Refuse(refusal.CodeInvalid,
			"app %s runs as %s, and the %q edge reaches no %s app on this provider: front this project with %s, or change %s's compute",
			app.App, compute, pre.Edge, compute, describeEdges(listPairedEdges(facts, compute)), app.App)
	}
	return nil
}

func listPairedEdges(facts provider.Facts, compute provider.Compute) []edge.Kind {
	var kinds []edge.Kind
	for _, pairing := range facts.Pairings {
		if slices.Contains(pairing.Computes, compute) && !slices.Contains(kinds, pairing.Edge) {
			kinds = append(kinds, pairing.Edge)
		}
	}
	slices.Sort(kinds)
	return kinds
}

func describeEdges(kinds []edge.Kind) string {
	quoted := make([]string, 0, len(kinds))
	for _, kind := range kinds {
		quoted = append(quoted, fmt.Sprintf("%q", kind))
	}
	return strings.Join(quoted, " or ")
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

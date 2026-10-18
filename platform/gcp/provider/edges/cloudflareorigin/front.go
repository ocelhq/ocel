package cloudflareorigin

import (
	"context"
	"fmt"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
)

type Front struct {
	edge.Edge
	Proxy     *cloudflare.Proxy
	Origin    edge.Edge
	Options   cloudflare.Options
	Wildcards Wildcards
}

func NewFront(namespace provider.Namespace, options cloudflare.Options, shielded *alb.Edge, wildcards Wildcards) Front {
	wildcards.OpenDNS = func() edge.DNSRecords { return cloudflare.NewDNS("") }
	wildcards.Balancer = shielded
	return Front{
		Edge:      cloudflare.NewWithWorkerClientCertificate(string(namespace), options, wildcards.TrustWorkerClientCertificate),
		Proxy:     cloudflare.NewProxy(string(namespace), options),
		Origin:    shielded,
		Options:   options,
		Wildcards: wildcards,
	}
}

func (f Front) Facts() edge.Facts {
	facts := f.Edge.Facts()
	facts.ShieldsOrigin = true
	return facts
}

func (f Front) Reconcile(ctx context.Context, spec edge.StackSpec, prior edge.StackState) (edge.EdgeStack, error) {
	base := f.Options.OriginBase(spec.Tier)
	if f.Facts().RunsCode && base != "" && (!spec.PruneOnly || spec.Tier == environment.TierPreview) {
		if err := f.Wildcards.Ensure(ctx, spec.Tier, base, spec.Warn); err != nil {
			if !spec.PruneOnly {
				return nil, err
			}
			if spec.Warn != nil {
				spec.Warn(fmt.Sprintf("the preview origin wildcard *.%s is not ready, and serverless apps behind the worker are refused until it is: %v", base, err))
			}
		}
	}
	return f.Edge.Reconcile(ctx, spec, prior)
}

func (f Front) Hooks() edge.Hooks {
	hooks := f.Edge.Hooks()
	proxied := f.Proxy.Hooks()
	hooks.OriginCertificates = proxied.OriginCertificates
	hooks.PurgeHostnames = proxied.PurgeHostnames
	origin := f.Origin.Hooks()
	hooks.CheckBootstrapInstalled = origin.CheckBootstrapInstalled
	hooks.ListBoundHostnames = origin.ListBoundHostnames
	return hooks
}

func (f Front) Bootstrap(ctx context.Context, tier environment.Tier) (edge.BootstrapOutput, error) {
	if _, err := f.Origin.Bootstrap(ctx, tier); err != nil {
		return edge.BootstrapOutput{}, err
	}
	return f.Edge.Bootstrap(ctx, tier)
}

func (f Front) Teardown(ctx context.Context, tier environment.Tier) error {
	if err := f.Wildcards.Destroy(ctx, tier); err != nil {
		return err
	}
	if err := f.Edge.Teardown(ctx, tier); err != nil {
		return err
	}
	return f.Origin.Teardown(ctx, tier)
}

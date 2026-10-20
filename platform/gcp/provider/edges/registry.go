package edges

import (
	"context"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/seal"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/cloudflareorigin"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/cloudrun"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/payloads"
	"github.com/ocelhq/ocel/platform/gcp/provider/pin"
)

type Registry struct {
	Namespace provider.Namespace
	KeyValues records.Store
	Pins      pin.Pins
	Gates     cloudrun.Gates
	Stacks    alb.Stacks
	Routes    alb.Routes
	Entries   alb.Entries
	WarmURL   func(ctx context.Context, url, address, cookie string) error
	Assets    func(ctx context.Context, tier environment.Tier) (alb.AssetsBucket, error)
	CopyFiles func(ctx context.Context, tier environment.Tier, from, to alb.StaticRoutes) error
	ProbeGate func(ctx context.Context, hostname, address string) (bool, error)
	Cipher    seal.Cipher
	Images    alb.PluginImages
	Project   string
	Region    string

	Certificates  provider.Certificates
	ReadWorkerCAs func(ctx context.Context, tier environment.Tier) ([]string, error)
	FrontPart     *alb.FrontPart
}

var Supported = []edge.Kind{alb.Kind, cloudflare.Kind}

func (e Registry) OpenCloudRun() *cloudrun.Edge {
	return cloudrun.New(string(e.Namespace), e.Pins, e.Gates)
}

func (e Registry) OpenALB() *alb.Edge {
	return alb.New(alb.Deps{
		Namespace: string(e.Namespace),
		KeyValues: e.KeyValues,
		Stacks:    e.Stacks,
		Routes:    e.Routes,
		Entries:   e.Entries,
		Pins:      e.Pins,
		WarmURL:   e.WarmURL,
		Project:   e.Project,
		Region:    e.Region,

		Cipher:     e.Cipher,
		GatePlugin: payloads.GatePlugin(),
		Images:     e.Images,
		ProbeGate:  e.ProbeGate,

		WaitBeforeRetry:    waitBeforeRetry,
		ReadAssetsBucket:   e.Assets,
		CopyImmutableFiles: e.CopyFiles,
		FrontPart:          e.FrontPart,
	})
}

func (e Registry) Open(kind edge.Kind, options provider.Options) (edge.Edge, error) {
	switch kind {
	case edge.None:
		return e.OpenCloudRun(), nil
	case alb.Kind:
		if _, err := provider.DecodeEdgeOptions[alb.Options](alb.Kind, options); err != nil {
			return nil, err
		}
		return e.OpenALB(), nil
	case cloudflare.Kind:
		decoded, err := cloudflare.DecodeOptions(options)
		if err != nil {
			return nil, err
		}
		return cloudflareorigin.NewFront(e.Namespace, decoded, e.OpenALB().Shielded(), cloudflareorigin.Wildcards{
			KeyValues:     e.KeyValues,
			Certificates:  e.Certificates,
			ReadWorkerCAs: e.ReadWorkerCAs,
		}), nil
	}
	return nil, refusal.Refuse(refusal.CodeInvalid,
		"this provider cannot front deployments with the %q edge: leave `provider.gcp.edge` out, and each service answers on the url Cloud Run gives it, "+
			"or name %s, which provisions one load balancer per bootstrap tier at %s",
		kind, alb.Kind, alb.BaselineCost)
}

func waitBeforeRetry(ctx context.Context, attempt int) bool {
	return googleretry.WaitedFor(ctx, attempt, gcloud.WaitCeiling)
}

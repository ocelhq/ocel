package edges

import (
	"context"
	"errors"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/seal"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/cloudrun"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/payloads"
	"github.com/ocelhq/ocel/platform/gcp/provider/pin"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

type Registry struct {
	Namespace   provider.Namespace
	KeyValues   records.Store
	Pins        pin.Pins
	Gates       cloudrun.Gates
	Stacks      alb.Stacks
	Routes      alb.Routes
	Entries     alb.Entries
	WarmURL     func(ctx context.Context, url, address, cookie string) error
	Assets      func(ctx context.Context, tier environment.Tier) (alb.AssetsBucket, error)
	CopyFiles   func(ctx context.Context, tier environment.Tier, from, to alb.StaticRoutes) error
	ProbeGate   func(ctx context.Context, hostname, address string) (bool, error)
	Cipher      seal.Cipher
	Images      alb.PluginImages
	Project     string
	ReadProject func(context.Context) (string, error)
	Region      string
	FrontPart   *alb.FrontPart
}

var Supported = []edge.Kind{alb.Kind, cloudflare.Kind}

func (e Registry) Cloudflare() cloudflare.Origin {
	origin := cloudflare.Origin{Scope: e.cloudflareScope}
	if e.KeyValues != nil {
		origin.Records = cloudflareRecords{Store: e.KeyValues}
	}
	return origin
}

type cloudflareRecords struct{ records.Store }

func (s cloudflareRecords) Read(ctx context.Context, key records.Key) (records.Entry, error) {
	entry, err := s.Store.Read(ctx, key)
	if errors.Is(err, ports.ErrNoRecordsDatabase) {
		return records.Entry{}, records.ErrNotFound
	}
	return entry, err
}

func (e Registry) cloudflareScope(ctx context.Context) (cloudflare.OriginScope, error) {
	project, err := e.ReadProject(ctx)
	if err != nil {
		return cloudflare.OriginScope{}, err
	}
	return cloudflare.OriginScope{Vendor: string(gcloud.Vendor), ID: project}, nil
}

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
		return cloudflare.New(string(e.Namespace), e.Cloudflare(), decoded), nil
	}
	return nil, refusal.Refuse(refusal.CodeInvalid,
		"this provider cannot front deployments with the %q edge: leave `provider.gcp.edge` out, and each service answers on the url Cloud Run gives it, "+
			"or name %s, which provisions one load balancer per bootstrap tier at %s",
		kind, alb.Kind, alb.BaselineCost)
}

func waitBeforeRetry(ctx context.Context, attempt int) bool {
	return googleretry.WaitedFor(ctx, attempt, gcloud.WaitCeiling)
}

package edges

import (
	"context"
	"errors"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/albresources"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/cloudrun"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/pin"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

type Registry struct {
	Namespace   provider.Namespace
	KeyValues   records.Store
	Pins        pin.Pins
	Gates       cloudrun.Gates
	ReadProject func(context.Context) (string, error)
	ALBDeps     func() alb.Deps
}

var Supported = []edge.Kind{albresources.Kind, cloudflare.Kind}

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

func (e Registry) OpenALB() *alb.Edge { return alb.New(e.ALBDeps()) }

func (e Registry) Open(kind edge.Kind, options provider.Options) (edge.Edge, error) {
	switch kind {
	case edge.None:
		return e.OpenCloudRun(), nil
	case albresources.Kind:
		if _, err := provider.DecodeEdgeOptions[alb.Options](albresources.Kind, options); err != nil {
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
		kind, albresources.Kind, albresources.BaselineCost)
}

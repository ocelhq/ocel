package control

import (
	"context"

	"github.com/ocelhq/ocel/platform/gcp/provider/certs"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/cloudrun"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
)

func (p *Provider) EdgeRegistry(albEngine func() alb.Engine) edges.Registry {
	return edges.Registry{
		Namespace:   p.Namespace,
		KeyValues:   p.KeyValues(),
		Pins:        p.Services(),
		Gates:       cloudrun.GateSecrets{OpenClients: p.OpenClients},
		ReadProject: p.Project,
		ALBDeps:     func() alb.Deps { return p.albDeps(albEngine()) },
	}
}

func (p *Provider) computeEngine() alb.Engine { return alb.NewComputeEngine(p.OpenClients) }

func (p *Provider) albDeps(engine alb.Engine) alb.Deps {
	warming := &alb.HTTPS{}
	return alb.Deps{
		Namespace: string(p.Namespace),
		KeyValues: p.KeyValues(),
		Routes:    &alb.URLMaps{OpenClients: p.OpenClients, Services: p.Services()},
		Entries:   certs.New(p.OpenClients),
		Pins:      p.Services(),
		WarmURL:   warming.Warm,
		Project:   p.Options.Project,
		Region:    p.Options.Region,

		Cipher:    p.Cipher(),
		ProbeGate: warming.ProbeGate,

		WaitBeforeRetry:    waitBeforeRetry,
		ReadAssetsBucket:   (&alb.AssetsBuckets{OpenClients: p.OpenClients}).Read,
		CopyImmutableFiles: p.Objects().CopyImmutableFiles,

		Engine: engine,
	}
}

func waitBeforeRetry(ctx context.Context, attempt int) bool {
	return googleretry.WaitedFor(ctx, attempt, gcloud.WaitCeiling)
}

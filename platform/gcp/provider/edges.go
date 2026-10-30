package gcp

import (
	"github.com/ocelhq/ocel/platform/gcp/provider/edges"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/albpart"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/albprogram"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/cloudflareorigin"
	"github.com/ocelhq/ocel/platform/gcp/provider/payloads"
)

//go:generate go generate -C ../../edge/cloudflare/deploy ./...

func (p *Provider) edges() edges.Registry { return p.EdgeRegistry(p.albEngine) }

func (p *Provider) albEngine() alb.Engine {
	return alb.Engine{
		Stacks:        albprogram.PulumiStacks{OpenClients: p.OpenClients, SkipChecks: p.SkipChecks},
		Images:        albpart.PluginImages{OpenClients: p.OpenClients},
		GatePlugin:    payloads.GatePlugin(),
		FrontPart:     albpart.Front(bootstrapGate{p}.openBootstrap),
		EvaluateFront: p.evaluateFront,
		ReadApps:      p.appEdges.Read,
	}
}

func (p *Provider) offers() cloudflareorigin.Offers {
	return cloudflareorigin.Offers{OpenClients: p.OpenClients, KeyValues: p.KeyValues()}
}

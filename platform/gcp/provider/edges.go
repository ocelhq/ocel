package gcp

import (
	"github.com/ocelhq/ocel/platform/gcp/provider/certs"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/albpart"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/cloudflareorigin"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/cloudrun"
)

//go:generate go generate -C ../../edge/cloudflare/deploy ./...

func (p *Provider) edges() edges.Registry {
	return edges.Registry{
		Namespace:   p.namespace,
		KeyValues:   p.KeyValues(),
		Pins:        p.services(),
		Gates:       cloudrun.GateSecrets{OpenClients: p.openClients},
		Stacks:      alb.PulumiStacks{OpenClients: p.openClients},
		Routes:      &alb.URLMaps{OpenClients: p.openClients, Services: p.services()},
		Entries:     certs.New(p.openClients),
		WarmURL:     (&alb.HTTPS{Roots: p.warmRoots}).Warm,
		Assets:      (&alb.AssetsBuckets{OpenClients: p.openClients}).Read,
		CopyFiles:   p.objects().CopyImmutableFiles,
		ProbeGate:   (&alb.HTTPS{Roots: p.warmRoots}).ProbeGate,
		Cipher:      p.Cipher(),
		Images:      albpart.PluginImages{OpenClients: p.openClients},
		Project:     p.options.Project,
		ReadProject: p.Project,
		Region:      p.options.Region,
		FrontPart:   albpart.Front(bootstrapGate{p}.openBootstrap),
	}
}

func (p *Provider) offers() cloudflareorigin.Offers {
	return cloudflareorigin.Offers{OpenClients: p.openClients, KeyValues: p.KeyValues()}
}

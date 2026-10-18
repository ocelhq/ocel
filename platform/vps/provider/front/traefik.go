package front

import (
	"context"
	"strconv"
	"strings"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/docker"
	"github.com/ocelhq/ocel/platform/vps/provider/machine"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/traefik"
)

func (t *Traefik) Named() string { return runBy(t.Preset, "Traefik") }

func (t *Traefik) Spelled() string {
	base := Traefik{Preset: t.Preset}.Filled()
	written, filled := t.entrypoints(), base.entrypoints()
	var fields, entrypoints spelling
	fields.text("preset", t.Preset, "")
	fields.text("directory", t.Directory, base.Directory)
	fields.text("containerDirectory", t.ContainerDirectory, base.ContainerDirectory)
	fields.text("resolver", t.Resolver, base.Resolver)
	fields.text("previewResolver", t.PreviewResolver, base.PreviewResolver)
	entrypoints.text("http", written.HTTP, filled.HTTP)
	entrypoints.text("https", written.HTTPS, filled.HTTPS)
	if len(entrypoints) > 0 {
		fields = append(fields, strconv.Quote("entrypoints")+": "+entrypoints.object())
	}
	fields.text("network", t.Network, base.Network)
	fields.number("port", t.Port, base.Port)
	return `{ "traefik": ` + fields.object() + ` }`
}

func (t *Traefik) Process() string { return "traefik " + t.Preset }

func (t *Traefik) Resolvers() string { return t.Resolver + " " + t.PreviewResolver }

func (t *Traefik) SharesPreviewWildcard() bool { return t.PreviewResolver != "" }

func (t *Traefik) DescribeSharedPreviewCertificate(base string) string {
	return "Set the option \"proxy.traefik.previewResolver\" to a resolver that issues " + edge.PreviewWildcard(base) + " over DNS-01, so previews share one wildcard certificate"
}

func (t *Traefik) Joined() []docker.Network { return joinedOther(t.Network, "proxy.traefik.network") }

func (t *Traefik) Published() []docker.Publish { return publishedOnLoopback(t.Network, t.Port) }

func (t *Traefik) Listening() []string { return httpsListening(t.Network) }

func (t *Traefik) DirectoryOption() string { return "proxy.traefik.directory" }

func (t *Traefik) Open(box Box) proxy.Proxy {
	entrypoints := t.entrypoints()
	return traefik.Traefik{
		Box:                box,
		Directory:          t.Directory,
		ContainerDirectory: t.ContainerDirectory,
		Resolver:           t.Resolver,
		PreviewResolver:    t.PreviewResolver,
		HTTP:               entrypoints.HTTP,
		HTTPS:              entrypoints.HTTPS,
		Network:            t.Network,
		Port:               t.Port,
	}
}

func (t *Traefik) ReloadGrant() []machine.Item { return nil }

func (t *Traefik) ReloadCommand() string { return "" }

func (t *Traefik) GrantedContainers() []string { return nil }

func (t *Traefik) RouteBy(string) string { return "" }

func (t *Traefik) ForwardedByHand() bool { return false }

func (t *Traefik) WatchesDirectory() bool { return true }

func (t *Traefik) RefuseUnreachable(ctx context.Context, _ Box, publishers func(ctx context.Context, port string) ([]string, error)) error {
	if t.Network != "" {
		return nil
	}
	containers, err := publishers(ctx, proxy.HTTPSPort)
	if err != nil || len(containers) == 0 {
		return err
	}
	named := strings.Join(containers, " and ")
	return refusal.Refuse(refusal.CodeNotReady,
		"container %s publishes :%s from a docker network, where %s:%d is its own loopback and not this box's, so it cannot reach ocel's switchboard\n"+
			"Set `proxy.traefik.network` to the docker network %s is on, or run %s with network_mode: host",
		named, proxy.HTTPSPort, LoopbackAddr, t.Port, named, named)
}

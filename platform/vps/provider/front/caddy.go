package front

import (
	"context"
	"strings"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/docker"
	"github.com/ocelhq/ocel/platform/vps/provider/machine"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddyfile"
	"github.com/ocelhq/ocel/platform/vps/provider/shell"
)

func (c *Caddy) Named() string { return runBy(c.Preset, "Caddy") }

func (c *Caddy) Spelled() string {
	base := Caddy{Preset: c.Preset}.Filled()
	var fields spelling
	fields.text("preset", c.Preset, "")
	fields.text("directory", c.Directory, base.Directory)
	fields.text("containerDirectory", c.ContainerDirectory, base.ContainerDirectory)
	fields.text("container", c.Container, base.Container)
	fields.text("config", c.Config, base.Config)
	fields.text("network", c.Network, base.Network)
	fields.number("port", c.Port, base.Port)
	return `{ "caddy": ` + fields.object() + ` }`
}

func (c *Caddy) Process() string { return "caddy " + c.Container }

func (c *Caddy) Resolvers() string { return "" }

func (c *Caddy) SharesPreviewWildcard() bool { return false }

func (c *Caddy) DescribeSharedPreviewCertificate(base string) string {
	return "Serve " + edge.PreviewWildcard(base) + " from a wildcard certificate in your Caddy"
}

func (c *Caddy) Joined() []docker.Network { return joinedOther(c.Network, "proxy.caddy.network") }

func (c *Caddy) Published() []docker.Publish { return publishedOnLoopback(c.Network, c.Port) }

func (c *Caddy) Listening() []string { return httpsListening(c.Network) }

func (c *Caddy) DirectoryOption() string { return "proxy.caddy.directory" }

func (c *Caddy) Open(box Box) proxy.Proxy { return c.caddyfile(box) }

func (c *Caddy) caddyfile(box caddyfile.Box) caddyfile.Caddyfile {
	return caddyfile.Caddyfile{
		Box:                box,
		Preset:             c.Preset,
		Directory:          c.Directory,
		ContainerDirectory: c.ContainerDirectory,
		Container:          c.Container,
		Config:             c.Config,
		Network:            c.Network,
		Port:               c.Port,
	}
}

func (c *Caddy) ReloadGrant() []machine.Item {
	if c.Container != "" {
		return nil
	}
	sudoers := []byte(boxapi.DeployUser + " ALL=(root) NOPASSWD: " + strings.Join(caddyfile.ServiceReload(), " ") + "\n")
	return []machine.Item{{Kind: machine.KindFile, Name: machine.SudoersCaddyReload, Mode: 0o440, Owner: machine.RootOwner, Content: sudoers,
		Note: "sudo for reloading your Caddy"}}
}

func (c *Caddy) ReloadCommand() string {
	reload := shell.Words(c.caddyfile(nil).Reloading())
	if c.Container == "" {
		return "if systemctl is-active --quiet caddy.service; then " + reload + "; fi"
	}
	return "if [ \"$(docker inspect --type container --format '{{.State.Running}}' " + shell.Quote(c.Container) + " 2>/dev/null)\" = true ]; then " + reload + "; fi"
}

func (c *Caddy) GrantedContainers() []string {
	if c.Container == "" {
		return nil
	}
	return []string{c.Container}
}

func (c *Caddy) RouteBy(string) string { return "" }

func (c *Caddy) ForwardedByHand() bool { return false }

func (c *Caddy) WatchesDirectory() bool { return false }

func (c *Caddy) RefuseUnreachable(ctx context.Context, box Box, _ func(ctx context.Context, port string) ([]string, error)) error {
	return c.caddyfile(box).RefuseUnreachable(ctx)
}

package host

import (
	"cmp"
	"context"
	"fmt"
	"path/filepath"
	"strconv"

	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/docker"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddy"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/manual"
	"github.com/ocelhq/ocel/platform/vps/provider/routing"
	"github.com/ocelhq/ocel/platform/vps/provider/shell"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

type Front struct {
	Manual  *ManualFront  `json:"manual,omitempty"`
	Traefik *TraefikFront `json:"traefik,omitempty"`
	Caddy   *CaddyFront   `json:"caddy,omitempty"`
}

type ManualFront struct {
	Port    int    `json:"port"`
	Network string `json:"network,omitempty"`
}

type TraefikFront struct {
	Preset             string      `json:"preset,omitempty"`
	Directory          string      `json:"directory"`
	ContainerDirectory string      `json:"containerDirectory,omitempty"`
	Resolver           string      `json:"resolver"`
	PreviewResolver    string      `json:"previewResolver,omitempty"`
	Entrypoints        Entrypoints `json:"entrypoints"`
	Network            string      `json:"network,omitempty"`
	Port               int         `json:"port,omitempty"`
}

type Entrypoints struct {
	HTTP  string `json:"http"`
	HTTPS string `json:"https"`
}

type CaddyFront struct {
	Preset             string `json:"preset,omitempty"`
	Directory          string `json:"directory"`
	ContainerDirectory string `json:"containerDirectory,omitempty"`
	Container          string `json:"container,omitempty"`
	Config             string `json:"config"`
	Network            string `json:"network,omitempty"`
	Port               int    `json:"port,omitempty"`
}

func openFront(front Front, box frontBox) proxy.Proxy {
	switch {
	case front.Manual != nil:
		return manual.Manual{Box: box, Port: front.Manual.Port}
	case front.Caddy != nil:
		return front.caddyfile(box)
	case front.Traefik != nil:
		return front.traefik(box)
	default:
		return caddy.Builtin{Box: box}
	}
}

func (f Front) adopted() bool { return f != Front{} }

func (h *Host) RouteBy(hostname string) string {
	byHand := h.proxyOption.Manual
	switch {
	case byHand == nil:
		return ""
	case byHand.Network == "":
		return manual.Route(hostname, byHand.Port)
	default:
		return manual.RouteOn(hostname, byHand.Port, byHand.Network, "http://"+boxapi.Switchboard+":"+routing.SwitchboardPort)
	}
}

func (f Front) joined() []docker.Network {
	switch {
	case f.Manual != nil && f.Manual.Network != "" && f.Manual.Network != ProxyNetwork:
		return []docker.Network{{Name: f.Manual.Network, Option: "proxy.manual.network"}}
	case f.Caddy != nil && f.Caddy.Network != "" && f.Caddy.Network != ProxyNetwork:
		return []docker.Network{{Name: f.Caddy.Network, Option: "proxy.caddy.network"}}
	case f.Traefik != nil && f.Traefik.Network != "" && f.Traefik.Network != ProxyNetwork:
		return []docker.Network{{Name: f.Traefik.Network, Option: "proxy.traefik.network"}}
	default:
		return nil
	}
}

func (f Front) published() []docker.Publish {
	switch {
	case f.Manual != nil:
		return []docker.Publish{{Addr: loopbackAddr, Port: strconv.Itoa(f.Manual.Port), Target: routing.SwitchboardPort}}
	case f.Caddy != nil && f.Caddy.Network == "":
		return []docker.Publish{{Addr: loopbackAddr, Port: strconv.Itoa(f.Caddy.Port), Target: boxapi.HTTPSListenPort}}
	case f.Traefik != nil && f.Traefik.Network == "":
		return []docker.Publish{{Addr: loopbackAddr, Port: strconv.Itoa(f.Traefik.Port), Target: boxapi.HTTPSListenPort}}
	default:
		return nil
	}
}

func (f Front) listening() []string {
	switch {
	case f.Manual != nil:
		relaying := []string{"--relay-network", ProxyNetwork}
		for _, joined := range f.joined() {
			relaying = append(relaying, "--relay-network", joined.Name)
		}
		return relaying
	case f.Caddy != nil:
		return []string{"--https-listen", cmp.Or(f.Caddy.Network, ProxyNetwork) + ":" + boxapi.HTTPSListenPort}
	case f.Traefik != nil:
		return []string{"--https-listen", cmp.Or(f.Traefik.Network, ProxyNetwork) + ":" + boxapi.HTTPSListenPort}
	default:
		return nil
	}
}

func (f Front) directoryOption() string {
	switch {
	case f.Traefik != nil:
		return "proxy.traefik.directory"
	case f.Caddy != nil:
		return "proxy.caddy.directory"
	default:
		return ""
	}
}

func (f Front) directoryPresent() string {
	at := f.resolvePlacedFile()
	if at == "" {
		return ""
	}
	dir := filepath.Dir(at)
	missing := fmt.Sprintf("option %q names the directory %s, which this box does not have: name the directory your proxy reads, or create it",
		f.directoryOption(), dir)
	return "if [ ! -d " + shell.Quote(dir) + " ]; then\n" +
		"printf '%s\\n' " + shell.Quote(missing) + " >&2\n" +
		"exit 1\n" +
		"fi\n"
}

func (h *Host) refuseDirectoryMissing(ctx context.Context, front Front) error {
	present := front.directoryPresent()
	if present == "" {
		return nil
	}
	_, err := h.Run(ctx, ssh.Root, "find the directory "+front.directoryOption()+" names", present, nil)
	return err
}

func (f Front) switchboardNote() string {
	return "routes what your proxy forwards " + f.describeForwarding()
}

func (f Front) describeForwarding() string {
	if published := f.published(); len(published) > 0 {
		return "to " + published[0].String()
	}
	joined := f.joined()
	if len(joined) == 0 {
		return "on the " + ProxyNetwork + " network"
	}
	return "on the " + joined[0].Name + " network"
}

const loopbackAddr = "127.0.0.1"

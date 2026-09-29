package host

import (
	"cmp"
	"slices"
	"strconv"

	"github.com/ocelhq/ocel/pkg/edge"

	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddy"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/manual"
	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
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
	Preset          string      `json:"preset,omitempty"`
	Directory       string      `json:"directory"`
	Resolver        string      `json:"resolver"`
	PreviewResolver string      `json:"previewResolver,omitempty"`
	Entrypoints     Entrypoints `json:"entrypoints"`
	Network         string      `json:"network,omitempty"`
	Port            int         `json:"port,omitempty"`
}

type Entrypoints struct {
	HTTP  string `json:"http"`
	HTTPS string `json:"https"`
}

type CaddyFront struct {
	Preset    string `json:"preset,omitempty"`
	Directory string `json:"directory"`
	Container string `json:"container,omitempty"`
	Config    string `json:"config"`
	Network   string `json:"network,omitempty"`
	Port      int    `json:"port,omitempty"`
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

func destination(front proxy.Proxy) string {
	if file := front.File(); file != ProxyConfig {
		return file
	}
	return ""
}

func (state RoutingTable) hostnames() []string {
	named := make([]string, 0, len(state.Claims)+2)
	for _, claim := range state.Claims {
		named = append(named, claim.Hostname)
	}
	if state.Connector != "" {
		named = append(named, state.Connector)
	}
	if state.PreviewBase != "" {
		named = append(named, edge.ProbeHostname(edge.PreviewWildcard(state.PreviewBase)))
	}
	slices.Sort(named)
	return slices.Compact(named)
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
		return manual.RouteOn(hostname, byHand.Port, byHand.Network, "http://"+SwitchboardContainer+":"+switchboardPort)
	}
}

type userNetwork struct {
	name   string
	option string
}

func (f Front) joined() []userNetwork {
	switch {
	case f.Manual != nil && f.Manual.Network != "" && f.Manual.Network != ProxyNetwork:
		return []userNetwork{{name: f.Manual.Network, option: "proxy.manual.network"}}
	case f.Caddy != nil && f.Caddy.Network != "" && f.Caddy.Network != ProxyNetwork:
		return []userNetwork{{name: f.Caddy.Network, option: "proxy.caddy.network"}}
	case f.Traefik != nil && f.Traefik.Network != "" && f.Traefik.Network != ProxyNetwork:
		return []userNetwork{{name: f.Traefik.Network, option: "proxy.traefik.network"}}
	default:
		return nil
	}
}

func (f Front) published() []publish {
	switch {
	case f.Manual != nil:
		return []publish{{addr: loopbackAddr, port: strconv.Itoa(f.Manual.Port), target: switchboardPort}}
	case f.Caddy != nil && f.Caddy.Network == "":
		return []publish{{addr: loopbackAddr, port: strconv.Itoa(f.Caddy.Port), target: switchboard.HTTPSListenPort}}
	case f.Traefik != nil && f.Traefik.Network == "":
		return []publish{{addr: loopbackAddr, port: strconv.Itoa(f.Traefik.Port), target: switchboard.HTTPSListenPort}}
	default:
		return nil
	}
}

func (f Front) listening() []string {
	switch {
	case f.Manual != nil:
		relaying := []string{"--relay-network", ProxyNetwork}
		for _, joined := range f.joined() {
			relaying = append(relaying, "--relay-network", joined.name)
		}
		return relaying
	case f.Caddy != nil:
		return []string{"--https-listen", cmp.Or(f.Caddy.Network, ProxyNetwork) + ":" + switchboard.HTTPSListenPort}
	case f.Traefik != nil:
		return []string{"--https-listen", cmp.Or(f.Traefik.Network, ProxyNetwork) + ":" + switchboard.HTTPSListenPort}
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

func (f Front) switchboardNote() string {
	if published := f.published(); len(published) > 0 {
		return "routes what your proxy forwards to " + published[0].String()
	}
	joined := f.joined()
	if len(joined) == 0 {
		return "routes what your proxy forwards on the " + ProxyNetwork + " network"
	}
	return "routes what your proxy forwards on the " + joined[0].name + " network"
}

const loopbackAddr = "127.0.0.1"

package host

import (
	"slices"
	"strconv"

	edge "github.com/ocelhq/ocel/platform/edge/contract"

	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddy"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/manual"
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
	if h.proxyOption.Manual == nil {
		return ""
	}
	return manual.Route(hostname, h.proxyOption.Manual.Port)
}

func (f Front) published() []publish {
	if f.Manual == nil {
		return nil
	}
	return []publish{{addr: loopbackAddr, port: strconv.Itoa(f.Manual.Port), target: switchboardPort}}
}

const loopbackAddr = "127.0.0.1"

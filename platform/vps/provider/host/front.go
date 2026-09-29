package host

import (
	"cmp"
	"context"
	"slices"
	"strconv"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/refusal"

	"github.com/ocelhq/ocel/platform/vps/provider/certs"
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
	case front.adopted():
		return unservedFront{named: front.named()}
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

type unservedFront struct{ named string }

func (u unservedFront) refused() error {
	return refusal.Refuse(refusal.CodeInvalid,
		"%s is not supported yet as the proxy fronting this box; route to ocel yourself with `\"proxy\": \"manual\"`", u.named)
}

func (unservedFront) Guarantees() proxy.Guarantees { return proxy.Guarantees{} }

func (u unservedFront) Render(proxy.Spec) ([]byte, error) { return nil, u.refused() }

func (unservedFront) File() string { return "" }

func (unservedFront) Unrendered([]byte, proxy.Permission) string { return "" }

func (u unservedFront) RefuseRouted(context.Context, []string) error { return u.refused() }

func (u unservedFront) Validate(context.Context, []byte) error { return u.refused() }

func (u unservedFront) Reload(context.Context) error { return u.refused() }

func (u unservedFront) Inspect(context.Context) (proxy.Checks, error) { return nil, u.refused() }

func (unservedFront) Certificate(context.Context, string) (proxy.Certificate, error) {
	return proxy.Certificate{Renewal: certs.AdoptedRenewal}, nil
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

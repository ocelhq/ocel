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
	Manual *Loopback `json:"manual,omitempty"`
}

type Loopback struct {
	Port int `json:"port"`
}

func openFront(front Front, box frontBox) proxy.Proxy {
	switch {
	case front.Manual != nil:
		return manual.Manual{Box: box, Port: front.Manual.Port}
	default:
		return caddy.Builtin{Box: box}
	}
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

func (f Front) adopted() bool { return f.Manual != nil }

func (h *Host) RouteBy(hostname string) string {
	if h.fronts.Manual == nil {
		return ""
	}
	return manual.Route(hostname, h.fronts.Manual.Port)
}

func (f Front) published() []publish {
	if f.Manual == nil {
		return nil
	}
	return []publish{{addr: loopbackAddr, port: strconv.Itoa(f.Manual.Port), target: switchboardPort}}
}

const loopbackAddr = "127.0.0.1"

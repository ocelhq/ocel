package front

import (
	"context"
	"strconv"

	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/docker"
	"github.com/ocelhq/ocel/platform/vps/provider/machine"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/manual"
	"github.com/ocelhq/ocel/platform/vps/provider/routing"
)

func (m *Manual) Named() string { return "a proxy you route yourself" }

func (m *Manual) Spelled() string {
	var fields spelling
	fields.number("port", m.Port, manual.DefaultPort)
	fields.text("network", m.Network, "")
	if len(fields) == 0 {
		return `"manual"`
	}
	return `{ "manual": ` + fields.object() + ` }`
}

func (m *Manual) Process() string { return "manual" }

func (m *Manual) Resolvers() string { return "" }

func (m *Manual) SharesPreviewWildcard() bool { return false }

func (m *Manual) DescribeSharedPreviewCertificate(base string) string { return pinAdvice(base) }

func (m *Manual) Joined() []docker.Network { return joinedOther(m.Network, "proxy.manual.network") }

func (m *Manual) Published() []docker.Publish {
	return []docker.Publish{{Addr: LoopbackAddr, Port: strconv.Itoa(m.Port), Target: routing.SwitchboardPort}}
}

func (m *Manual) Listening() []string {
	relaying := []string{"--relay-network", boxapi.ProxyNetwork}
	for _, joined := range m.Joined() {
		relaying = append(relaying, "--relay-network", joined.Name)
	}
	return relaying
}

func (m *Manual) DirectoryOption() string { return "" }

func (m *Manual) Open(box Box) proxy.Proxy { return manual.Manual{Box: box, Port: m.Port} }

func (m *Manual) ReloadGrant() []machine.Item { return nil }

func (m *Manual) ReloadCommand() string { return "" }

func (m *Manual) GrantedContainers() []string { return nil }

func (m *Manual) RouteBy(hostname string) string {
	if m.Network == "" {
		return manual.Route(hostname, m.Port)
	}
	return manual.RouteOn(hostname, m.Port, m.Network, "http://"+boxapi.Switchboard+":"+routing.SwitchboardPort)
}

func (m *Manual) ForwardedByHand() bool { return true }

func (m *Manual) WatchesDirectory() bool { return false }

func (m *Manual) RefuseUnreachable(context.Context, Box, func(context.Context, string) ([]string, error)) error {
	return nil
}

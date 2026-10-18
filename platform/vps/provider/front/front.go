package front

import (
	"context"
	"strconv"
	"strings"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/docker"
	"github.com/ocelhq/ocel/platform/vps/provider/machine"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddy"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddyfile"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/manual"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/traefik"
)

type Front interface {
	Named() string
	Spelled() string
	Process() string
	Resolvers() string
	SharesPreviewWildcard() bool
	DescribeSharedPreviewCertificate(base string) string
	Joined() []docker.Network
	Published() []docker.Publish
	Listening() []string
	DirectoryOption() string
	Open(box Box) proxy.Proxy
	ReloadGrant() []machine.Item
	ReloadCommand() string
	GrantedContainers() []string
	RouteBy(hostname string) string
	ForwardedByHand() bool
	WatchesDirectory() bool
	RefuseUnreachable(ctx context.Context, box Box, publishers func(ctx context.Context, port string) ([]string, error)) error
}

type Box interface {
	caddy.Box
	caddyfile.Box
	traefik.Box
	manual.Box
}

const LoopbackAddr = "127.0.0.1"

type Builtin struct{}

func (Builtin) Named() string { return "ocel's own proxy" }

func (Builtin) Spelled() string { return "" }

func (Builtin) Process() string { return "" }

func (Builtin) Resolvers() string { return "" }

func (Builtin) SharesPreviewWildcard() bool { return false }

func (Builtin) DescribeSharedPreviewCertificate(base string) string { return pinAdvice(base) }

func (Builtin) Joined() []docker.Network { return nil }

func (Builtin) Published() []docker.Publish { return nil }

func (Builtin) Listening() []string { return nil }

func (Builtin) DirectoryOption() string { return "" }

func (Builtin) Open(box Box) proxy.Proxy { return caddy.Builtin{Box: box} }

func (Builtin) ReloadGrant() []machine.Item { return nil }

func (Builtin) ReloadCommand() string { return "" }

func (Builtin) GrantedContainers() []string { return nil }

func (Builtin) RouteBy(string) string { return "" }

func (Builtin) ForwardedByHand() bool { return false }

func (Builtin) WatchesDirectory() bool { return false }

func (Builtin) RefuseUnreachable(context.Context, Box, func(context.Context, string) ([]string, error)) error {
	return nil
}

func pinAdvice(base string) string {
	return "Pin a certificate for " + edge.PreviewWildcard(base) + " under the option \"certificates\", so previews share it"
}

func joinedOther(network, option string) []docker.Network {
	if network == "" || network == boxapi.ProxyNetwork {
		return nil
	}
	return []docker.Network{{Name: network, Option: option}}
}

func publishedOnLoopback(network string, port int) []docker.Publish {
	if network != "" {
		return nil
	}
	return []docker.Publish{{Addr: LoopbackAddr, Port: strconv.Itoa(port), Target: boxapi.HTTPSListenPort}}
}

func httpsListening(network string) []string {
	if network == "" {
		network = boxapi.ProxyNetwork
	}
	return []string{"--https-listen", network + ":" + boxapi.HTTPSListenPort}
}

type spelling []string

func (s *spelling) text(key, value, base string) {
	if value != "" && value != base {
		*s = append(*s, strconv.Quote(key)+": "+strconv.Quote(value))
	}
}

func (s *spelling) number(key string, value, base int) {
	if value != 0 && value != base {
		*s = append(*s, strconv.Quote(key)+": "+strconv.Itoa(value))
	}
}

func (s spelling) object() string { return "{ " + strings.Join(s, ", ") + " }" }

func DescribeForwarding(f Front) string {
	if published := f.Published(); len(published) > 0 {
		return "to " + published[0].String()
	}
	joined := f.Joined()
	if len(joined) == 0 {
		return "on the " + boxapi.ProxyNetwork + " network"
	}
	return "on the " + joined[0].Name + " network"
}

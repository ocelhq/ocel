package caddyfile

import (
	"net"
	"strconv"
	"strings"

	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

func (c Caddyfile) upstream() string {
	if c.Network != "" {
		return net.JoinHostPort(switchboard.Name, switchboard.HTTPSListenPort)
	}
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(c.Port))
}

func (c Caddyfile) render(hostnames []string) []byte {
	if len(hostnames) == 0 {
		return []byte{}
	}
	return []byte(strings.Join(hostnames, ", ") + " {\n\treverse_proxy " + c.upstream() + "\n}\n")
}

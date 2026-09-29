package caddyfile

import (
	"net"
	"strconv"
	"strings"

	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

const streamCloseDelay = "30s"

func (c Caddyfile) upstream() string {
	if c.Network != "" {
		return net.JoinHostPort(switchboard.Name, switchboard.HTTPSListenPort)
	}
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(c.Port))
}

func (c Caddyfile) render(hostnames []string) []byte {
	if len(hostnames) == 0 {
		return []byte("\n")
	}
	if c.Container == "" {
		return []byte(strings.Join(hostnames, ", ") + " {\n\treverse_proxy " + c.upstream() + "\n}\n")
	}
	return []byte(strings.Join(hostnames, ", ") + " {\n\treverse_proxy " + c.upstream() + " {\n\t\tstream_close_delay " + streamCloseDelay + "\n\t}\n}\n")
}

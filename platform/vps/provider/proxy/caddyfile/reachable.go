package caddyfile

import (
	"context"
	"strings"

	"github.com/ocelhq/ocel/pkg/refusal"
)

const hostNetwork = "host"

func (c Caddyfile) RefuseUnreachable(ctx context.Context) error {
	if c.Container != "" && c.Network == "" {
		said, err := c.Box.Ran(ctx, "read the network mode of "+c.Container,
			[]string{"docker", "inspect", "--type", "container", "--format", "{{.HostConfig.NetworkMode}}", c.Container})
		if err != nil {
			return err
		}
		if mode := strings.TrimSpace(said); mode != hostNetwork {
			return refusal.Refuse(refusal.CodeNotReady,
				"%s runs with network mode %s, where 127.0.0.1:%d is its own loopback and not this box's, so it cannot reach ocel's switchboard\n"+
					"Set `proxy.caddy.network` to a docker network %s is on, or run it with network_mode: host",
				c.named(), mode, c.Port, c.Container)
		}
	}
	_, err := c.sites(ctx)
	return err
}

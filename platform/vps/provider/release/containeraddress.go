package release

import (
	"context"
	"net"
	"strings"

	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/shell"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

func (c *Containers) Forward(ctx context.Context, name, port string) (string, func(), error) {
	address, err := c.ReadAddress(ctx, name)
	if err != nil {
		return "", nil, err
	}
	return c.box.ForwardPort(ctx, net.JoinHostPort(address, port))
}

func ContainerAddressCommand(name string) string {
	return "docker inspect --type container --format " + shell.Quote("{{range .NetworkSettings.Networks}}{{.IPAddress}} {{end}}") + " " + shell.Quote(name)
}

func (c *Containers) ReadAddress(ctx context.Context, name string) (string, error) {
	elevation, err := ssh.Docker, c.box.Assume(ctx, ssh.Docker)
	if err != nil {
		return "", err
	}
	said, err := c.box.Run(ctx, elevation, "read the address of container "+name, ContainerAddressCommand(name), nil)
	if err != nil {
		return "", err
	}
	addresses := strings.Fields(said)
	if len(addresses) == 0 {
		return "", refusal.Refuse(refusal.CodeNotReady,
			"container %s on %s holds no address on any network, so it is not running and no port can be forwarded to it", name, c.box.Named())
	}
	return addresses[0], nil
}

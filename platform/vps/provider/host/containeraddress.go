package host

import (
	"context"
	"net"
	"strings"

	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/shell"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

func (h *Host) ForwardToContainer(ctx context.Context, name, port string) (string, func(), error) {
	address, err := h.ReadContainerAddress(ctx, name)
	if err != nil {
		return "", nil, err
	}
	return h.ForwardPort(ctx, net.JoinHostPort(address, port))
}

func containerAddressCommand(name string) string {
	return "docker inspect --type container --format " + shell.Quote("{{range .NetworkSettings.Networks}}{{.IPAddress}} {{end}}") + " " + shell.Quote(name)
}

func (h *Host) ReadContainerAddress(ctx context.Context, name string) (string, error) {
	elevation, err := ssh.Docker, h.Assume(ctx, ssh.Docker)
	if err != nil {
		return "", err
	}
	said, err := h.Run(ctx, elevation, "read the address of container "+name, containerAddressCommand(name), nil)
	if err != nil {
		return "", err
	}
	addresses := strings.Fields(said)
	if len(addresses) == 0 {
		return "", refusal.Refuse(refusal.CodeNotReady,
			"container %s on %s holds no address on any network, so it is not running and no port can be forwarded to it", name, h.Named())
	}
	return addresses[0], nil
}

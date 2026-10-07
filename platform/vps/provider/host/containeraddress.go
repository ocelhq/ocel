package host

import (
	"context"
	"net"
	"strings"

	"github.com/ocelhq/ocel/pkg/refusal"
)

func (h *Host) ForwardToContainer(ctx context.Context, name, port string) (string, func(), error) {
	address, err := h.ReadContainerAddress(ctx, name)
	if err != nil {
		return "", nil, err
	}
	live, err := h.dial(ctx)
	if err != nil {
		return "", nil, err
	}
	return live.ForwardPort(ctx, net.JoinHostPort(address, port))
}

func containerAddressCommand(name string) string {
	return "docker inspect --type container --format " + quoted("{{range .NetworkSettings.Networks}}{{.IPAddress}} {{end}}") + " " + quoted(name)
}

func (h *Host) ReadContainerAddress(ctx context.Context, name string) (string, error) {
	elevation, err := h.reachDocker(ctx)
	if err != nil {
		return "", err
	}
	said, err := h.ran(ctx, "read the address of container "+name, containerAddressCommand(name), nil, elevation)
	if err != nil {
		return "", err
	}
	addresses := strings.Fields(said)
	if len(addresses) == 0 {
		return "", refusal.Refuse(refusal.CodeNotReady,
			"container %s on %s holds no address on any network, so no port can be forwarded to it: deploy again to start it", name, h.named())
	}
	return addresses[0], nil
}

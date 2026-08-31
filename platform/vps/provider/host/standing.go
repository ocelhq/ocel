package host

import (
	"context"
	"strings"

	"github.com/ocelhq/ocel/platform/vps/provider/listeners"
)

const listenerCommand = "cat " + listeners.TCPPath + " " + listeners.TCP6Path

func (h *Host) ProxyListeners(ctx context.Context) ([]listeners.Listener, error) {
	elevation, err := h.reachDocker(ctx)
	if err != nil {
		return nil, err
	}
	said, err := h.ran(ctx, "read what listens inside "+ProxyContainer,
		words(helperCommand("listeners")), nil, elevation)
	if err != nil {
		return nil, err
	}
	return listeners.Read(said)
}

func (h *Host) Listening(ctx context.Context) ([]listeners.Listener, error) {
	said, err := h.ran(ctx, "read what listens on this host", listenerCommand, nil, "")
	if err != nil {
		return nil, err
	}
	return listeners.Parse(strings.NewReader(said))
}

func publishing(port string) []string {
	return []string{"docker", "ps", "--filter", "publish=" + port, "--format", "{{.Names}}"}
}

func (h *Host) Publishing(ctx context.Context, port string) ([]string, error) {
	elevation, err := h.reachDocker(ctx)
	if err != nil {
		return nil, err
	}
	said, err := h.ran(ctx, "ask which container publishes port "+port, words(publishing(port)), nil, elevation)
	if err != nil {
		return nil, err
	}
	var named []string
	for line := range strings.Lines(said) {
		if name := strings.TrimSpace(line); name != "" {
			named = append(named, name)
		}
	}
	return named, nil
}

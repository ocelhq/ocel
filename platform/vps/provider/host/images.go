package host

import (
	"context"
	"io"
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

const dockerReach = "docker version"

func (h *Host) reachDocker(ctx context.Context) (string, error) {
	h.engining.Lock()
	defer h.engining.Unlock()
	if h.engined {
		return h.engine, nil
	}
	result, err := h.stream(ctx, dockerReach+" >/dev/null 2>&1", nil, "")
	if err != nil {
		return "", err
	}
	if result.Code != 0 {
		elevation, err := h.elevate(ctx)
		if err != nil {
			return "", err
		}
		h.engine = elevation
	}
	h.engined = true
	return h.engine, nil
}

func (h *Host) HoldsImage(ctx context.Context, coordinate string) (bool, error) {
	elevation, err := h.reachDocker(ctx)
	if err != nil {
		return false, err
	}
	named, err := h.ran(ctx, "ask whether "+coordinate+" stands", "docker image ls -q "+quoted(coordinate), nil, elevation)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(named) != "", nil
}

func (h *Host) LoadImage(ctx context.Context, coordinate string, tar io.Reader) (string, error) {
	elevation, err := h.reachDocker(ctx)
	if err != nil {
		return "", err
	}
	said, err := h.ran(ctx, "load "+coordinate, "docker load", tar, elevation)
	if err != nil {
		return "", err
	}
	held, err := h.HoldsImage(ctx, coordinate)
	if err != nil {
		return "", err
	}
	if !held {
		return "", providerkit.Refuse(providerkit.CodeInvalid,
			"%s took the image stream and answers to no %s afterwards, so nothing can be released under that coordinate: %s",
			h.named(), coordinate, strings.TrimSpace(said))
	}
	return strings.TrimSpace(said), nil
}

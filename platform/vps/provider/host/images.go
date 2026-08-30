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
	result, err := h.stream(ctx, dockerReach+" >/dev/null", nil, "")
	if err != nil {
		return "", err
	}
	if result.Code != 0 {
		if !deniedSocket(result.Stderr) {
			return "", providerkit.Refuse(providerkit.CodeNotReady,
				"%s cannot run docker, and an image reaches this machine through its daemon: %s",
				h.named(), spoken(result))
		}
		elevation, err := h.elevate(ctx)
		if err != nil {
			return "", err
		}
		h.engine = elevation
	}
	h.engined = true
	return h.engine, nil
}

func deniedSocket(stderr string) bool {
	return strings.Contains(strings.ToLower(stderr), "permission denied")
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

func (h *Host) PullImage(ctx context.Context, target providerkit.RegistryTarget, coordinate string) (string, error) {
	command, secret, err := pull(target, coordinate)
	if err != nil {
		return "", err
	}
	elevation, err := h.reachDocker(ctx)
	if err != nil {
		return "", err
	}
	said, err := h.ran(ctx, "pull "+coordinate+" from "+target.Server, command, secret, elevation)
	if err != nil {
		return "", err
	}
	held, err := h.HoldsImage(ctx, coordinate)
	if err != nil {
		return "", err
	}
	if !held {
		return "", providerkit.Refuse(providerkit.CodeInvalid,
			"%s pulled from %s and answers to no %s afterwards, so nothing can be released under that coordinate: %s",
			h.named(), target.Server, coordinate, strings.TrimSpace(said))
	}
	return strings.TrimSpace(said), nil
}

func pull(target providerkit.RegistryTarget, coordinate string) (string, io.Reader, error) {
	fetch := "docker pull " + quoted(coordinate)
	if target.Password == "" {
		return fetch, nil, nil
	}
	if target.Username == "" {
		return "", nil, providerkit.Refuse(providerkit.CodeInvalid,
			"%s is reached with a password and no username to present it under, and a docker login takes both: "+
				"name `username` beside `password` in the project's `registry`", target.Server)
	}
	return strings.Join([]string{
		"set -e",
		`config=$(mktemp -d)`,
		`trap 'docker logout ` + quoted(target.Server) + ` >/dev/null 2>&1; rm -rf "$config"' EXIT`,
		`export DOCKER_CONFIG="$config"`,
		"docker login --username " + quoted(target.Username) + " --password-stdin " + quoted(target.Server),
		fetch,
	}, "\n"), strings.NewReader(target.Password), nil
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

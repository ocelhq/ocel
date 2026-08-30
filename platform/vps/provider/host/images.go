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

func (h *Host) PullImage(ctx context.Context, target providerkit.RegistryTarget, coordinate, digest string) (string, error) {
	command, secret, err := pull(target, coordinate, digest)
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

func LoginStands(target providerkit.RegistryTarget) error {
	if target.Password == "" || target.Username != "" {
		return nil
	}
	return providerkit.Refuse(providerkit.CodeInvalid,
		"%s is reached with a password and no username to present it under, and a docker login takes both: "+
			"name `username` beside `password` in the project's `registry`", target.Server)
}

func pull(target providerkit.RegistryTarget, coordinate, digest string) (string, io.Reader, error) {
	pinned, err := pinnedTo(coordinate, digest)
	if err != nil {
		return "", nil, err
	}
	var secret io.Reader
	steps := []string{"set -e"}
	if target.Password != "" {
		if err := LoginStands(target); err != nil {
			return "", nil, err
		}
		secret = strings.NewReader(target.Password)
		steps = append(steps,
			`config=$(mktemp -d)`,
			`trap 'rm -rf "$config"; docker logout `+quoted(target.Server)+` >/dev/null 2>&1 || true' EXIT`,
			`export DOCKER_CONFIG="$config"`,
			"docker login --username "+quoted(target.Username)+" --password-stdin "+quoted(target.Server),
		)
	}
	steps = append(steps,
		"docker pull "+quoted(pinned),
		"docker tag "+quoted(pinned)+" "+quoted(coordinate),
	)
	return strings.Join(steps, "\n"), secret, nil
}

func pinnedTo(coordinate, digest string) (string, error) {
	if digest == "" {
		return "", providerkit.Refuse(providerkit.CodeInvalid,
			"%s pins no digest, and a tag is whatever the registry was last told it is: ocel pulls what this deploy built or nothing",
			coordinate)
	}
	repository := coordinate
	slash := strings.LastIndex(coordinate, "/")
	if colon := strings.LastIndex(coordinate[slash+1:], ":"); colon >= 0 {
		repository = coordinate[:slash+1+colon]
	}
	return repository + "@" + digest, nil
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

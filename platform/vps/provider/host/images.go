package host

import (
	"context"
	"io"
	"math/rand/v2"
	"strings"
	"time"

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
	command, err := pull(target, coordinate, digest)
	if err != nil {
		return "", err
	}
	elevation, err := h.reachDocker(ctx)
	if err != nil {
		return "", err
	}
	said, err := h.pulling(ctx, target, coordinate, command, elevation)
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

const (
	pullAttempts = 5
	pullBackoff  = 250 * time.Millisecond
	pullCeiling  = 8 * time.Second
)

func (h *Host) pulling(ctx context.Context, target providerkit.RegistryTarget, coordinate, command, elevation string) (string, error) {
	what := "pull " + coordinate + " from " + target.Server
	var said, stderr string
	var err error
	for attempt := range pullAttempts {
		if attempt > 0 {
			if waited := waiting(ctx, attempt); waited != nil {
				return "", waited
			}
		}
		var secret io.Reader
		if target.Password != "" {
			secret = strings.NewReader(target.Password)
		}
		if said, stderr, err = h.spoke(ctx, what, command, secret, elevation); err == nil || !providerkit.Throttled(stderr) {
			return said, err
		}
	}
	return "", err
}

func waiting(ctx context.Context, attempt int) error {
	wait := pullBackoff << (attempt - 1)
	if wait > pullCeiling {
		wait = pullCeiling
	}
	timer := time.NewTimer(wait/2 + time.Duration(rand.Int64N(int64(wait/2)+1)))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func LoginStands(target providerkit.RegistryTarget) error {
	if target.Password == "" || target.Username != "" {
		return nil
	}
	return providerkit.Refuse(providerkit.CodeInvalid,
		"%s is reached with a password and no username to present it under, and a docker login takes both: "+
			"name `username` beside `password` in the project's `registry`", target.Server)
}

func pull(target providerkit.RegistryTarget, coordinate, digest string) (string, error) {
	pinned, err := pinnedTo(coordinate, digest)
	if err != nil {
		return "", err
	}
	steps := []string{"set -e"}
	if target.Password != "" {
		if err := LoginStands(target); err != nil {
			return "", err
		}
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
	return strings.Join(steps, "\n"), nil
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
	said, err := h.ran(ctx, "load "+coordinate, "flock -x "+quoted(imagesLock)+" docker load", tar, elevation)
	if err != nil {
		return "", err
	}
	held, err := h.HoldsImage(ctx, coordinate)
	if err != nil {
		return "", err
	}
	if !held {
		return "", providerkit.Refuse(providerkit.CodeInvalid,
			"%s took the image stream and answers to no %s afterwards, so nothing can be released under that coordinate: %s\nwhat the box's engine says about it:\n%s",
			h.named(), coordinate, strings.TrimSpace(said), h.said(ctx, loadEvidenceCommand(), elevation))
	}
	return strings.TrimSpace(said), nil
}

const imagesLock = stateRoot + "/images.lock"

func loadEvidenceCommand() string {
	return strings.Join([]string{
		"docker events --since 10m --until 1s 2>&1 | tail -n 40",
		"echo '--- disk'; df -h /var/lib/docker /var/lib/containerd 2>&1",
		"echo '--- memory'; free -m 2>&1",
		"echo '--- images'; docker system df 2>&1",
	}, "\n") + "\n"
}

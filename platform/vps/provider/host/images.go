package host

import (
	"context"
	"io"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/ocelhq/ocel/pkg/images"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

const dockerReach = "docker version"

const StaticImage = "gcr.io/distroless/static-debian12@sha256:d75cdd72874d4790092fcb1b058493ecf6bb5bf2b2b897045b00ff01d91843f2"

func (h *Host) reachDocker(ctx context.Context) (string, error) {
	h.engining.Lock()
	defer h.engining.Unlock()
	if h.engined {
		return h.engine, nil
	}
	if acting, ok := h.actAsStateOwner(ctx); ok {
		result, err := h.stream(ctx, dockerReach+" >/dev/null", nil, acting)
		if err != nil {
			return "", err
		}
		if result.Code != 0 {
			return "", refusal.Refuse(refusal.CodeNotReady,
				"%s cannot run docker as %s: %s",
				h.named(), stateOwner, spoken(result))
		}
		h.engine, h.engined = acting, true
		return h.engine, nil
	}
	result, err := h.stream(ctx, dockerReach+" >/dev/null", nil, "")
	if err != nil {
		return "", err
	}
	if result.Code != 0 {
		if !deniedSocket(result.Stderr) {
			return "", refusal.Refuse(refusal.CodeNotReady,
				"%s cannot run docker: %s",
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

func (h *Host) HasImage(ctx context.Context, imageRef string) (bool, error) {
	elevation, err := h.reachDocker(ctx)
	if err != nil {
		return false, err
	}
	named, err := h.ran(ctx, "ask whether the engine has "+imageRef, "docker image ls -q "+quoted(imageRef), nil, elevation)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(named) != "", nil
}

func (h *Host) PullImage(ctx context.Context, target provider.RegistryTarget, imageRef, digest string) (string, error) {
	command, err := pull(target, imageRef, digest)
	if err != nil {
		return "", err
	}
	elevation, err := h.reachDocker(ctx)
	if err != nil {
		return "", err
	}
	if err := h.sweep(ctx, elevation); err != nil {
		return "", err
	}
	said, err := h.pulling(ctx, target, imageRef, command, elevation)
	if err != nil {
		return "", err
	}
	has, err := h.HasImage(ctx, imageRef)
	if err != nil {
		return "", err
	}
	if !has {
		return "", refusal.Refuse(refusal.CodeInvalid,
			"%s pulled from %s but has no %s: %s",
			h.named(), target.Server, imageRef, strings.TrimSpace(said))
	}
	return strings.TrimSpace(said), nil
}

const (
	pullAttempts = 5
	pullBackoff  = 250 * time.Millisecond
	pullCeiling  = 8 * time.Second
)

func (h *Host) pulling(ctx context.Context, target provider.RegistryTarget, imageRef, command, elevation string) (string, error) {
	what := "pull " + imageRef + " from " + target.Server
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
		if said, stderr, err = h.spoke(ctx, what, command, secret, elevation); err == nil || !images.RetryablePush(stderr) {
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

func CheckLogin(target provider.RegistryTarget) error {
	if target.Password == "" || target.Username != "" {
		return nil
	}
	return refusal.Refuse(refusal.CodeInvalid,
		"%s has a password but no username\nSet `username` beside `password` in the project's `registry`", target.Server)
}

func pull(target provider.RegistryTarget, imageRef, digest string) (string, error) {
	pinned, err := pinnedTo(imageRef, digest)
	if err != nil {
		return "", err
	}
	steps := []string{"set -e"}
	if target.Password != "" {
		if err := CheckLogin(target); err != nil {
			return "", err
		}
		steps = append(steps,
			`config=$(mktemp -d `+quoted(stateRoot+"/"+registryPrefix+"XXXXXX")+`)`,
			`trap 'rm -rf "$config"; docker logout `+quoted(target.Server)+` >/dev/null 2>&1 || true' EXIT`,
			`export DOCKER_CONFIG="$config"`,
			"docker login --username "+quoted(target.Username)+" --password-stdin "+quoted(target.Server),
		)
	}
	steps = append(steps,
		"docker pull "+quoted(pinned),
		"docker tag "+quoted(pinned)+" "+quoted(imageRef),
	)
	return strings.Join(steps, "\n"), nil
}

func pinnedTo(imageRef, digest string) (string, error) {
	if digest == "" {
		return "", refusal.Refuse(refusal.CodeInvalid,
			"%s pins no digest",
			imageRef)
	}
	repository := imageRef
	slash := strings.LastIndex(imageRef, "/")
	if colon := strings.LastIndex(imageRef[slash+1:], ":"); colon >= 0 {
		repository = imageRef[:slash+1+colon]
	}
	return repository + "@" + digest, nil
}

func (h *Host) LoadImage(ctx context.Context, imageRef string, tar io.Reader) (string, error) {
	acting, err := h.reachStateRoot(ctx)
	if err != nil {
		return "", err
	}
	said, err := h.ran(ctx, "load "+imageRef, acting+"flock -x "+quoted(imagesLock)+" docker load", tar, "")
	if err != nil {
		return "", err
	}
	has, err := h.HasImage(ctx, imageRef)
	if err != nil {
		return "", err
	}
	if !has {
		elevation, _ := h.reachDocker(ctx)
		return "", refusal.Refuse(refusal.CodeInvalid,
			"%s loaded the image but has no %s: %s\nengine state:\n%s",
			h.named(), imageRef, strings.TrimSpace(said), h.said(ctx, loadEvidenceCommand(), elevation))
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

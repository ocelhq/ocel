package release

import (
	"context"
	"fmt"
	"io"
	"math/rand/v2"
	"strings"
	"time"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/ocelhq/ocel/pkg/images"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/envfile"
	"github.com/ocelhq/ocel/platform/vps/provider/shell"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

func (i *Images) Has(ctx context.Context, imageRef string) (bool, error) {
	elevation, err := ssh.Docker, i.box.Assume(ctx, ssh.Docker)
	if err != nil {
		return false, err
	}
	named, err := i.box.Run(ctx, elevation, "ask whether the engine has "+imageRef, "docker image ls -q "+shell.Quote(imageRef), nil)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(named) != "", nil
}

func (i *Images) Pull(ctx context.Context, target provider.RegistryTarget, imageRef, digest string) (string, error) {
	command, err := Pull(target, imageRef, digest)
	if err != nil {
		return "", err
	}
	elevation, err := ssh.Docker, i.box.Assume(ctx, ssh.Docker)
	if err != nil {
		return "", err
	}
	if err := envfile.Sweep(ctx, i.box, elevation); err != nil {
		return "", err
	}
	said, err := i.pulling(ctx, target, imageRef, command, elevation)
	if err != nil {
		return "", err
	}
	has, err := i.Has(ctx, imageRef)
	if err != nil {
		return "", err
	}
	if !has {
		return "", refusal.Refuse(refusal.CodeInvalid,
			"%s pulled from %s but has no %s: %s",
			i.box.Named(), target.Server, imageRef, strings.TrimSpace(said))
	}
	return strings.TrimSpace(said), nil
}

const (
	PullAttempts = 5
	pullBackoff  = 250 * time.Millisecond
	pullCeiling  = 8 * time.Second
)

func (i *Images) pulling(ctx context.Context, target provider.RegistryTarget, imageRef, command string, elevation ssh.Acting) (string, error) {
	what := "pull " + imageRef + " from " + target.Server
	var said, stderr string
	var err error
	for attempt := range PullAttempts {
		if attempt > 0 {
			if waited := waiting(ctx, attempt); waited != nil {
				return "", waited
			}
		}
		var secret io.Reader
		if target.Password != "" {
			secret = strings.NewReader(target.Password)
		}
		if said, stderr, err = i.box.Attempt(ctx, elevation, what, command, secret); err == nil || !images.RetryablePush(stderr) {
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

func Pull(target provider.RegistryTarget, imageRef, digest string) (string, error) {
	pinned, err := pinnedTo(imageRef, digest)
	if err != nil {
		return "", err
	}
	steps := []string{"set -e"}
	if target.Password != "" {
		if err := CheckLogin(target); err != nil {
			return "", err
		}
		steps = append(steps, "docker "+boxapi.DockerPullAs+" "+shell.Words([]string{target.Server, target.Username, pinned}))
	} else {
		steps = append(steps, "docker pull "+shell.Quote(pinned))
	}
	steps = append(steps, "docker tag "+shell.Quote(pinned)+" "+shell.Quote(imageRef))
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

func (i *Images) Load(ctx context.Context, imageRef string, built v1.Image, archive io.Reader) (string, error) {
	config, err := built.ConfigName()
	if err != nil {
		return "", err
	}
	manifest, err := built.Digest()
	if err != nil {
		return "", err
	}
	said, err := i.box.Run(ctx, ssh.Deploy, "load "+imageRef, "flock -x "+shell.Quote(boxapi.ImagesLock)+" docker load", archive)
	if err != nil {
		return "", err
	}
	has, err := i.Has(ctx, imageRef)
	if err != nil {
		return "", err
	}
	if !has {
		elevation, _ := ssh.Docker, i.box.Assume(ctx, ssh.Docker)
		return "", refusal.Refuse(refusal.CodeInvalid,
			"%s loaded the image but has no %s: %s\nengine state:\n%s",
			i.box.Named(), imageRef, strings.TrimSpace(said), i.box.Evidence(ctx, elevation, loadEvidenceCommand()))
	}
	id, err := i.box.Run(ctx, ssh.Docker, "ask which image "+imageRef+" names", ImageIDCommand(imageRef), nil)
	if err != nil {
		return "", err
	}
	if loaded := strings.TrimSpace(id); loaded != config.String() && loaded != manifest.String() {
		mismatch := fmt.Sprintf("%s names %s as %s after the load, not the image this deploy sent (config %s, manifest %s)",
			i.box.Named(), imageRef, loaded, config, manifest)
		if _, err := i.box.Run(ctx, ssh.Docker, "untag "+imageRef, "docker image rm -f "+shell.Quote(imageRef), nil); err != nil {
			return "", refusal.Refuse(refusal.CodeInvalid,
				"%s, and the tag could not be removed: %v\nRun `docker image rm -f %s` on %s before deploying again", mismatch, err, imageRef, i.box.Named())
		}
		return "", refusal.Refuse(refusal.CodeInvalid, "%s\nThe tag is removed, so the next deploy sends the image again", mismatch)
	}
	return strings.TrimSpace(said), nil
}

func ImageIDCommand(imageRef string) string {
	return "docker image inspect --format " + shell.Quote("{{.Id}}") + " " + shell.Quote(imageRef)
}

func loadEvidenceCommand() string {
	return strings.Join([]string{
		"docker events --since 10m --until 1s 2>&1 | tail -n 40",
		"echo '--- disk'; df -h /var/lib/docker /var/lib/containerd 2>&1",
		"echo '--- memory'; free -m 2>&1",
		"echo '--- images'; docker system df 2>&1",
	}, "\n") + "\n"
}

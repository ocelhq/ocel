package providerkit

import (
	"context"
	"fmt"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/daemon"
)

type daemonImages struct{}

func DaemonImages() ImageStore { return daemonImages{} }

func (daemonImages) String() string { return "images written to the local docker daemon" }

func (d daemonImages) GoString() string { return d.String() }

func (daemonImages) ImageDestination() string { return "the local docker daemon" }

func (daemonImages) Has(ctx context.Context, push ImagePush) (bool, error) {
	ref, err := name.NewTag(push.Target, name.Insecure)
	if err != nil {
		return false, fmt.Errorf("%q names nowhere the daemon can hold an image: %w", push.Target, err)
	}
	held, err := daemon.Image(ref, daemon.WithContext(ctx))
	if err != nil {
		return false, nil
	}
	digest, err := held.Digest()
	return err == nil && digest.String() == push.Digest, nil
}

func (daemonImages) Push(ctx context.Context, push ImagePush, _ Reporter) error {
	if push.Built == nil {
		return Refuse(CodeInvalid,
			"%s's image is written straight into the local docker daemon, and this release carries no image it was built into", push.App)
	}
	ref, err := name.NewTag(push.Target, name.Insecure)
	if err != nil {
		return fmt.Errorf("%q names nowhere the daemon can hold an image: %w", push.Target, err)
	}
	if _, err := daemon.Write(ref, push.Built, daemon.WithContext(ctx)); err != nil {
		return fmt.Errorf("write %s's image into the local docker daemon: %w", push.App, err)
	}
	return nil
}

package providerkit

import (
	"context"
	"fmt"

	"github.com/containerd/errdefs"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/daemon"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type daemonImages struct{}

func DaemonImages() ImageStore { return daemonImages{} }

func (daemonImages) String() string { return "images written to the local docker daemon" }

func (d daemonImages) GoString() string { return d.String() }

func (daemonImages) Destination() string { return "the local docker daemon" }

func (daemonImages) Has(ctx context.Context, push ImagePush) (bool, error) {
	ref, err := name.NewTag(push.ImageRef, name.Insecure)
	if err != nil {
		return false, fmt.Errorf("%q names nowhere the daemon can hold an image: %w", push.ImageRef, err)
	}
	_, err = daemon.Image(ref, daemon.WithContext(ctx))
	if errdefs.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("ask the local docker daemon for %s: %w", push.ImageRef, err)
	}
	return true, nil
}

func (daemonImages) Push(ctx context.Context, push ImagePush, _ edge.Progress) error {
	if push.Built == nil {
		return refusal.Refuse(refusal.CodeInvalid,
			"%s's image is written straight into the local docker daemon, and this release carries no image it was built into", push.App)
	}
	ref, err := name.NewTag(push.ImageRef, name.Insecure)
	if err != nil {
		return fmt.Errorf("%q names nowhere the daemon can hold an image: %w", push.ImageRef, err)
	}
	if _, err := daemon.Write(ref, push.Built, daemon.WithContext(ctx)); err != nil {
		return fmt.Errorf("write %s's image into the local docker daemon: %w", push.App, err)
	}
	return nil
}

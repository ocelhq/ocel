package vps

import (
	"context"
	"fmt"
	"io"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/tarball"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

type loaded struct {
	host *host.Host
	at   string
}

func (p *Provider) OpenDirectImages(context.Context) (providerkit.ImageStore, error) {
	return loaded{host: p.host, at: p.options.SSH.session().Destination()}, nil
}

func (l loaded) String() string { return "images loaded onto " + l.at }

func (l loaded) GoString() string { return l.String() }

func (l loaded) Destination() string { return l.at }

func (l loaded) Has(ctx context.Context, push providerkit.ImagePush) (bool, error) {
	return l.host.HoldsImage(ctx, push.ImageRef)
}

func (l loaded) Push(ctx context.Context, push providerkit.ImagePush, progress edge.Progress) error {
	if push.Built == nil {
		return refusal.Refuse(refusal.CodeInvalid,
			"%s: this release carries no built image to load onto the box", push.App)
	}
	return l.load(ctx, push, progress)
}

func (l loaded) load(ctx context.Context, push providerkit.ImagePush, progress edge.Progress) error {
	ref, err := name.NewTag(push.ImageRef, name.Insecure)
	if err != nil {
		return fmt.Errorf("%q is not a valid image tag: %w", push.ImageRef, err)
	}
	stream, writer := io.Pipe()
	go func() { writer.CloseWithError(tarball.Write(ref, push.Built, writer)) }()
	said, err := l.host.LoadImage(ctx, push.ImageRef, stream)
	_ = stream.Close()
	if err != nil {
		return err
	}
	if progress != nil && said != "" {
		progress.Detail(said)
	}
	return nil
}

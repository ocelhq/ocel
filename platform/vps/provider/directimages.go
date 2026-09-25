package vps

import (
	"context"
	"fmt"
	"io"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/tarball"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

type loaded struct {
	host *host.Host
	at   string
}

func (p *Provider) DirectImages(context.Context) (providerkit.ImageStore, error) {
	return loaded{host: p.host, at: p.options.SSH.session().Destination()}, nil
}

func (l loaded) String() string { return "images loaded onto " + l.at }

func (l loaded) GoString() string { return l.String() }

func (l loaded) Destination() string { return l.at }

func (l loaded) Has(ctx context.Context, push providerkit.ImagePush) (bool, error) {
	return l.host.HoldsImage(ctx, push.Target)
}

func (l loaded) Push(ctx context.Context, push providerkit.ImagePush, report providerkit.Reporter) error {
	if push.Built == nil {
		return providerkit.Refuse(providerkit.CodeInvalid,
			"%s: this release carries no built image to load onto the box", push.App)
	}
	return l.load(ctx, push, report)
}

func (l loaded) load(ctx context.Context, push providerkit.ImagePush, report providerkit.Reporter) error {
	ref, err := name.NewTag(push.Target, name.Insecure)
	if err != nil {
		return fmt.Errorf("%q is not a valid image tag: %w", push.Target, err)
	}
	stream, writer := io.Pipe()
	go func() { writer.CloseWithError(tarball.Write(ref, push.Built, writer)) }()
	said, err := l.host.LoadImage(ctx, push.Target, stream)
	_ = stream.Close()
	if err != nil {
		return err
	}
	if report != nil && said != "" {
		report.Detail(said)
	}
	return nil
}

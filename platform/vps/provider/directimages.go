package vps

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/tarball"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

type loaded struct {
	host *host.Host
	at   string
	now  func() time.Time
}

func (p *Provider) OpenDirectImages(context.Context) (provider.ImageStore, error) {
	return loaded{host: p.host, at: p.options.SSH.session().Destination(), now: p.now}, nil
}

func (l loaded) String() string { return "images loaded onto " + l.at }

func (l loaded) GoString() string { return l.String() }

func (l loaded) Destination() string { return l.at }

func (loaded) CheckPush(context.Context, string) error { return nil }

func (l loaded) Has(ctx context.Context, push provider.ImagePush) (bool, error) {
	return l.host.HasImage(ctx, push.ImageRef)
}

func (l loaded) Push(ctx context.Context, push provider.ImagePush, progress progress.Log) error {
	if push.Built == nil {
		return refusal.Refuse(refusal.CodeInvalid,
			"%s: this release includes no built image to load onto the box", push.App)
	}
	return l.load(ctx, push, progress)
}

func (l loaded) load(ctx context.Context, push provider.ImagePush, progress progress.Log) error {
	ref, err := name.NewTag(push.ImageRef, name.Insecure)
	if err != nil {
		return fmt.Errorf("%q is not a valid image tag: %w", push.ImageRef, err)
	}
	stream, writer := io.Pipe()
	go func() { writer.CloseWithError(tarball.Write(ref, push.Built, writer)) }()
	sent := &counted{from: stream}
	began := l.now()
	said, err := l.host.LoadImage(ctx, push.ImageRef, sent)
	took := l.now().Sub(began)
	_ = stream.Close()
	if err != nil {
		return err
	}
	echo(progress, said)
	if progress != nil && (took >= slowTransfer || sent.bytes >= largeTransfer) {
		progress.Warn(fmt.Sprintf(
			"Sending %s's image to %s took %s for %d MB. With no registry every deploy sends the whole image over SSH: name a `registry` and the box pulls only the layers that changed. %s",
			push.App, l.at, took.Round(time.Second), sent.bytes/1_000_000, registryDocs))
	}
	return nil
}

const (
	slowTransfer  = 30 * time.Second
	largeTransfer = 300_000_000
	registryDocs  = "https://ocel.dev/docs/providers/vps#images"
)

type counted struct {
	from  io.Reader
	bytes int64
}

func (c *counted) Read(p []byte) (int, error) {
	n, err := c.from.Read(p)
	c.bytes += int64(n)
	return n, err
}

package vps

import (
	"context"
	"net/http"

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

func (l loaded) ImageDestination() string { return l.at }

func (l loaded) Has(ctx context.Context, push providerkit.ImagePush) (bool, error) {
	return l.host.HoldsImage(ctx, push.Target)
}

func (l loaded) Push(ctx context.Context, push providerkit.ImagePush, report providerkit.Reporter) error {
	daemon, err := providerkit.OpenDockerHost()
	if err != nil {
		return err
	}
	transport := daemon.Transport()
	defer transport.CloseIdleConnections()

	stream, err := daemon.Export(ctx, &http.Client{Transport: transport}, push.Target)
	if err != nil {
		return err
	}
	defer func() { _ = stream.Close() }()

	said, err := l.host.LoadImage(ctx, push.Target, stream)
	if err != nil {
		return err
	}
	if report != nil && said != "" {
		report.Detail(said)
	}
	return nil
}

var _ providerkit.ImageLoader = (*Provider)(nil)

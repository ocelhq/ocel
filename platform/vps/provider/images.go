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

type pulled struct {
	host   *host.Host
	at     string
	from   providerkit.ImageStore
	target providerkit.RegistryTarget
}

func (p *Provider) Images(_ context.Context, target providerkit.RegistryTarget) (providerkit.ImageStore, error) {
	return pulled{
		host:   p.host,
		at:     p.options.SSH.session().Destination(),
		from:   providerkit.RegistryImages(target),
		target: target,
	}, nil
}

func (p pulled) String() string { return "images pulled onto " + p.at + " from " + p.target.Server }

func (p pulled) GoString() string { return p.String() }

func (p pulled) ImageDestination() string { return p.at }

func (p pulled) Has(ctx context.Context, push providerkit.ImagePush) (bool, error) {
	return p.host.HoldsImage(ctx, push.Target)
}

func (p pulled) Push(ctx context.Context, push providerkit.ImagePush, report providerkit.Reporter) error {
	held, err := p.from.Has(ctx, push)
	if err != nil {
		return err
	}
	if !held {
		if err := p.from.Push(ctx, push, report); err != nil {
			return err
		}
	}
	said, err := p.host.PullImage(ctx, p.target, push.Target)
	if err != nil {
		return err
	}
	if report != nil && said != "" {
		report.Detail(said)
	}
	return nil
}

var (
	_ providerkit.ImageLoader = (*Provider)(nil)
	_ providerkit.ImagePusher = (*Provider)(nil)
)

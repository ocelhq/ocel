package vps

import (
	"context"
	"fmt"

	"github.com/ocelhq/ocel/pkg/providerkit/images"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

type pulled struct {
	host   *host.Host
	at     string
	from   provider.ImageStore
	target provider.RegistryTarget
}

func (p *Provider) OpenRegistryImages(_ context.Context, target provider.RegistryTarget) (provider.ImageStore, error) {
	if err := host.CheckLogin(target); err != nil {
		return nil, err
	}
	return pulled{
		host:   p.host,
		at:     p.options.SSH.session().Destination(),
		from:   images.RegistryStore(target),
		target: target,
	}, nil
}

func (p pulled) String() string { return "images pulled onto " + p.at + " from " + p.target.Server }

func (p pulled) GoString() string { return p.String() }

func (p pulled) Destination() string { return p.at }

func (p pulled) Has(ctx context.Context, push provider.ImagePush) (bool, error) {
	return p.host.HasImage(ctx, push.ImageRef)
}

func (p pulled) Push(ctx context.Context, push provider.ImagePush, progress edge.Progress) error {
	present, err := p.from.Has(ctx, push)
	if err != nil {
		return err
	}
	if !present {
		if err := p.from.Push(ctx, push, progress); err != nil {
			return err
		}
	}
	digest := push.Digest
	if push.Built != nil {
		built, err := push.Built.Digest()
		if err != nil {
			return fmt.Errorf("read the digest of %s's wrapped image: %w", push.App, err)
		}
		digest = built.String()
	}
	said, err := p.host.PullImage(ctx, p.target, push.ImageRef, digest)
	if err != nil {
		return err
	}
	if progress != nil && said != "" {
		progress.Detail(said)
	}
	return nil
}

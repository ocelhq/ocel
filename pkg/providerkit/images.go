package providerkit

import (
	"context"
	"fmt"
	"strings"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/ocelhq/ocel/pkg/naming"
)

const ImageKind = "image"

type ImagePush struct {
	App      string
	Source   string
	ImageRef string
	Digest   string

	Function bool
	Built    v1.Image
	Wrap     Wrapped
}

type ImageStore interface {
	Destination() string

	Has(ctx context.Context, push ImagePush) (bool, error)

	Push(ctx context.Context, push ImagePush, progress Progress) error
}

type ImagePlan struct {
	Store  ImageStore
	Pushes []ImagePush
}

func (p ImagePlan) String() string {
	targets := make([]string, 0, len(p.Pushes))
	for _, push := range p.Pushes {
		targets = append(targets, push.App+" to "+push.ImageRef)
	}
	if len(targets) == 0 {
		return "no image push"
	}
	return "images pushing " + strings.Join(targets, ", ")
}

func (p ImagePlan) GoString() string { return p.String() }

func (p ImagePlan) Rows(ctx context.Context) ([]Change, error) {
	rows := make([]Change, 0, len(p.Pushes))
	for _, push := range p.Pushes {
		held, err := p.held(ctx, push)
		if err != nil {
			return nil, err
		}
		rows = append(rows, Change{Kind: ImageKind, Name: push.App, Action: standsOrCreates(held)})
	}
	return rows, nil
}

func (p ImagePlan) Ship(ctx context.Context, progress Progress) error {
	for _, push := range p.Pushes {
		held, err := p.held(ctx, push)
		if err != nil {
			return err
		}
		if held {
			continue
		}
		where := p.Store.Destination()
		if progress != nil {
			progress.Say("Sending " + push.App + "'s image to " + where)
		}
		if err := p.push(ctx, push, progress); err != nil {
			return fmt.Errorf("send %s's image to %s: %w", push.App, where, err)
		}
	}
	return nil
}

func (p ImagePlan) push(ctx context.Context, push ImagePush, progress Progress) error {
	if push.Wrap != nil {
		if progress != nil {
			progress.Detail("Wrapping the image in the ocel runtime")
		}
		built, done, err := push.Wrap(ctx)
		if err != nil {
			return err
		}
		defer done()
		push.Built = built
	}
	return p.Store.Push(ctx, push, progress)
}

func (p ImagePlan) ImageRef(app string) string {
	for _, push := range p.Pushes {
		if !push.Function && push.App == app {
			return push.ImageRef
		}
	}
	return ""
}

func (p ImagePlan) held(ctx context.Context, push ImagePush) (bool, error) {
	if p.Store == nil {
		return false, Refuse(CodeInvalid,
			"%s's image is pushed to %s and this release carries nothing to push it with", push.App, push.ImageRef)
	}
	held, err := p.Store.Has(ctx, push)
	if err != nil {
		return false, fmt.Errorf("look for %s's image in %s: %w", push.App, p.Store.Destination(), err)
	}
	return held, nil
}

func imageRef(repository, tag string, target RegistryTarget) string {
	if !target.Named() {
		return repository + ":" + tag
	}
	return target.ImageRef(naming.RepositorySegment(repository), tag)
}

func imageStoreFor(ctx context.Context, provider Provider, target RegistryTarget) (ImageStore, error) {
	hooks := provider.Hooks()
	if !target.Named() {
		if hooks.OpenDirectImages == nil {
			return nil, nil
		}
		return hooks.OpenDirectImages(ctx)
	}
	if hooks.OpenRegistryImages != nil {
		return hooks.OpenRegistryImages(ctx, target)
	}
	return RegistryImages(target), nil
}

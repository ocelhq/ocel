package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit/images"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const ImageKind = "image"

type ImagePushes struct {
	Store  images.Store
	Pushes []images.Push
}

func (p ImagePushes) String() string {
	targets := make([]string, 0, len(p.Pushes))
	for _, push := range p.Pushes {
		targets = append(targets, push.App+" to "+push.ImageRef)
	}
	if len(targets) == 0 {
		return "no image push"
	}
	return "images pushing " + strings.Join(targets, ", ")
}

func (p ImagePushes) GoString() string { return p.String() }

func (p ImagePushes) Rows(ctx context.Context) ([]Change, error) {
	rows := make([]Change, 0, len(p.Pushes))
	for _, push := range p.Pushes {
		held, err := p.held(ctx, push)
		if err != nil {
			return nil, err
		}
		rows = append(rows, Change{Kind: ImageKind, Name: push.App, Action: KeepOrCreate(held)})
	}
	return rows, nil
}

func (p ImagePushes) PushMissing(ctx context.Context, progress edge.Progress) error {
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

func (p ImagePushes) push(ctx context.Context, push images.Push, progress edge.Progress) error {
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

func (p ImagePushes) ImageRef(app string) string {
	for _, push := range p.Pushes {
		if !push.Function && push.App == app {
			return push.ImageRef
		}
	}
	return ""
}

func (p ImagePushes) held(ctx context.Context, push images.Push) (bool, error) {
	if p.Store == nil {
		return false, refusal.Refuse(refusal.CodeInvalid,
			"%s's image is pushed to %s and this release carries nothing to push it with", push.App, push.ImageRef)
	}
	held, err := p.Store.Has(ctx, push)
	if err != nil {
		return false, fmt.Errorf("look for %s's image in %s: %w", push.App, p.Store.Destination(), err)
	}
	return held, nil
}

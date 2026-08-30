package providerkit

import (
	"context"
	"fmt"
	"strings"

	"github.com/ocelhq/ocel/pkg/naming"
)

const ImageKind = "image"

type ImagePush struct {
	App    string
	Source string
	Target string
	Digest string
}

type ImageStore interface {
	Has(ctx context.Context, push ImagePush) (bool, error)

	Push(ctx context.Context, push ImagePush, report Reporter) error
}

type ImagePusher interface {
	Images(ctx context.Context, target RegistryTarget) (ImageStore, error)
}

type ImageLoader interface {
	DirectImages(ctx context.Context) (ImageStore, error)
}

type ImageDestination interface {
	ImageDestination() string
}

type ImagePlan struct {
	Store  ImageStore
	Pushes []ImagePush
}

func (p ImagePlan) String() string {
	targets := make([]string, 0, len(p.Pushes))
	for _, push := range p.Pushes {
		targets = append(targets, push.App+" to "+push.Target)
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

func (p ImagePlan) Ship(ctx context.Context, report Reporter) error {
	for _, push := range p.Pushes {
		held, err := p.held(ctx, push)
		if err != nil {
			return err
		}
		if held {
			continue
		}
		if report != nil {
			report.Say("Sending " + push.App + "'s image to " + p.destination(push))
		}
		if err := p.Store.Push(ctx, push, report); err != nil {
			return fmt.Errorf("push %s's image to %s: %w", push.App, push.Target, err)
		}
	}
	return nil
}

func (p ImagePlan) destination(push ImagePush) string {
	if named, says := p.Store.(ImageDestination); says {
		return named.ImageDestination()
	}
	return push.Target
}

func (p ImagePlan) held(ctx context.Context, push ImagePush) (bool, error) {
	if p.Store == nil {
		return false, Refuse(CodeInvalid,
			"%s's image is pushed to %s and this release carries nothing to push it with", push.App, push.Target)
	}
	held, err := p.Store.Has(ctx, push)
	if err != nil {
		return false, fmt.Errorf("look for %s's image in %s: %w", push.App, push.Target, err)
	}
	return held, nil
}

func imagePush(app, ref string, target RegistryTarget) (ImagePush, error) {
	repository, digest, pinned := strings.Cut(ref, "@")
	if !pinned || repository == "" || digest == "" {
		return ImagePush{}, Refuse(CodeInvalid,
			"app %s carries the image %q, which pins no digest, so there is nothing to push under a coordinate", app, ref)
	}
	return ImagePush{
		App:    app,
		Source: ref,
		Target: coordinate(repository, naming.DigestTag(digest), target),
		Digest: digest,
	}, nil
}

func coordinate(repository, tag string, target RegistryTarget) string {
	if target.Server == "" {
		return repository + ":" + tag
	}
	return target.Coordinate(naming.RepositorySegment(repository), tag)
}

func imageStoreFor(ctx context.Context, provider Provider, target RegistryTarget) (ImageStore, error) {
	if target.Server == "" {
		loading, takes := provider.(ImageLoader)
		if !takes {
			return nil, nil
		}
		return loading.DirectImages(ctx)
	}
	if pushing, own := provider.(ImagePusher); own {
		return pushing.Images(ctx, target)
	}
	return RegistryImages(target), nil
}

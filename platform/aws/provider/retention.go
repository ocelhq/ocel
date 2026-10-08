package aws

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/ecr"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/aws/provider/registry"
)

const imageReclaimGrace = 30 * time.Minute

func (p *Provider) ReconcileImages(ctx context.Context, ref provider.StackRef, app, imageRef string, _ provider.ImageStore, log progress.Log) error {
	kept, err := reconciledKeptImages(ctx, p.KeyValues(), ref)
	if err != nil {
		return err
	}
	removed, err := registry.Reconcile(ctx, ecr.NewFromConfig(p.aws), imageRef, kept, time.Now().Add(-imageReclaimGrace))
	resources.SayRemovedImages(log, app, removed)
	return err
}

func (p *Provider) ForgetReleases(ctx context.Context, ref provider.StackRef, app string, images provider.ImageStore, log progress.Log) error {
	return forgetImages(ctx, p.KeyValues(), ecr.NewFromConfig(p.aws), ref, app, images, log)
}

func forgetImages(ctx context.Context, store keyvalue.Store, api registry.ECRAPI, ref provider.StackRef, app string, images provider.ImageStore, log progress.Log) error {
	recorded, found, err := stackrecords.Read(ctx, store, ref.Tier, ref.Project, ref.Name)
	if err != nil || !found {
		return err
	}
	kept, err := forgottenKeptImages(ctx, store, ref)
	if err != nil {
		return err
	}
	var pushed, ours []string
	for _, image := range imagesOf(recorded) {
		switch {
		case kept[image]:
		case images != nil && strings.HasPrefix(image, images.Destination()+"/"):
			pushed = append(pushed, image)
		default:
			ours = append(ours, image)
		}
	}
	resources.RemovePushedImages(ctx, images, app, pushed, log)
	if len(ours) == 0 {
		return nil
	}
	removed, err := registry.Forget(ctx, api, ours, kept, time.Now().Add(-imageReclaimGrace))
	resources.SayRemovedImages(log, app, removed)
	return err
}

func reconciledKeptImages(ctx context.Context, store keyvalue.Store, ref provider.StackRef) (map[string]bool, error) {
	return recordedImages(ctx, store, ref.Project, func(environment.Tier, naming.StackName) bool { return true })
}

func forgottenKeptImages(ctx context.Context, store keyvalue.Store, ref provider.StackRef) (map[string]bool, error) {
	return recordedImages(ctx, store, ref.Project, func(tier environment.Tier, name naming.StackName) bool {
		return tier != ref.Tier || name != ref.Name
	})
}

func recordedImages(ctx context.Context, store keyvalue.Store, project string, counted func(environment.Tier, naming.StackName) bool) (map[string]bool, error) {
	recorded := map[string]bool{}
	for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
		stacks, err := stackrecords.List(ctx, store, tier, project)
		if err != nil {
			return nil, err
		}
		for _, stack := range stacks {
			if !counted(tier, stack.Name) {
				continue
			}
			for _, image := range imagesOf(stack.Stack) {
				recorded[image] = true
			}
		}
	}
	return recorded, nil
}

func imagesOf(recorded stackrecords.Stack) []string {
	var images []string
	if recorded.Image != "" {
		images = append(images, recorded.Image)
	}
	for _, container := range recorded.Containers {
		if container.Image != "" && !slices.Contains(images, container.Image) {
			images = append(images, container.Image)
		}
	}
	return images
}

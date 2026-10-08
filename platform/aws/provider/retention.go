package aws

import (
	"context"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/ecr"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/aws/provider/registry"
)

const imageReclaimGrace = 30 * time.Minute

func (p *Provider) ReconcileImages(ctx context.Context, ref provider.StackRef, app, imageRef string, _ provider.ImageStore, log progress.Log) error {
	standing, err := standingImages(ctx, p.KeyValues(), ref)
	if err != nil {
		return err
	}
	removed, err := registry.Reconcile(ctx, ecr.NewFromConfig(p.aws), imageRef, standing, time.Now().Add(-imageReclaimGrace))
	sayRemoved(log, app, removed)
	return err
}

func (p *Provider) ForgetReleases(ctx context.Context, ref provider.StackRef, app string, log progress.Log) error {
	recorded, found, err := stackrecords.Read(ctx, p.KeyValues(), ref.Tier, ref.Project, ref.Name)
	if err != nil || !found {
		return err
	}
	var images []string
	for _, container := range recorded.Containers {
		if container.Image != "" {
			images = append(images, container.Image)
		}
	}
	if len(images) == 0 {
		return nil
	}
	standing, err := standingImages(ctx, p.KeyValues(), ref)
	if err != nil {
		return err
	}
	removed, err := registry.Forget(ctx, ecr.NewFromConfig(p.aws), images, standing)
	sayRemoved(log, app, removed)
	return err
}

func standingImages(ctx context.Context, store keyvalue.Store, ref provider.StackRef) (map[string]bool, error) {
	standing := map[string]bool{}
	for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
		stacks, err := stackrecords.List(ctx, store, tier, ref.Project)
		if err != nil {
			return nil, err
		}
		for _, stack := range stacks {
			if tier == ref.Tier && stack.Name == ref.Name {
				continue
			}
			for _, container := range stack.Containers {
				if container.Image != "" {
					standing[container.Image] = true
				}
			}
		}
	}
	return standing, nil
}

func sayRemoved(log progress.Log, app string, removed []string) {
	if log == nil {
		return
	}
	for _, image := range removed {
		log.Say("Removed " + app + "'s unused image " + image)
	}
}

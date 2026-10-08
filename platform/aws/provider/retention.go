package aws

import (
	"context"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/ecr"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
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
	kept, err := forgottenKeptImages(ctx, p.KeyValues(), ref)
	if err != nil {
		return err
	}
	removed, err := registry.Forget(ctx, ecr.NewFromConfig(p.aws), images, kept)
	sayRemoved(log, app, removed)
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
			for _, container := range stack.Containers {
				if container.Image != "" {
					recorded[container.Image] = true
				}
			}
		}
	}
	return recorded, nil
}

func sayRemoved(log progress.Log, app string, removed []string) {
	if log == nil {
		return
	}
	for _, image := range removed {
		log.Say("Removed " + app + "'s unused image " + image)
	}
}

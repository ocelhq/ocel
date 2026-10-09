package aws

import (
	"context"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/ecr"

	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/aws/provider/registry"
)

const imageReclaimGrace = 30 * time.Minute

func (p *Provider) ReconcileImages(ctx context.Context, ref provider.StackRef, app, imageRef string, _ provider.ImageStore, log progress.Log) error {
	removed, err := registry.Reconcile(ctx, ecr.NewFromConfig(p.aws), imageRef, otherStacksImages(p.KeyValues(), ref), time.Now().Add(-imageReclaimGrace))
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
	if err := stackrecords.ForgetAppImages(ctx, store, ref.Tier, ref.Project, ref.Name); err != nil {
		return err
	}
	if len(recorded.Images()) == 0 {
		return nil
	}
	keptNow := otherStacksImages(store, ref)
	kept, err := keptNow(ctx)
	if err != nil {
		return err
	}
	var unkept []string
	for _, image := range recorded.Images() {
		if !kept[image] {
			unkept = append(unkept, image)
		}
	}
	if len(unkept) == 0 {
		return nil
	}
	target, err := registry.Resolve(ctx, api)
	if err != nil {
		return err
	}
	var pushed, ours []string
	for _, image := range unkept {
		switch {
		case registry.IsInRegistry(target, image):
			ours = append(ours, image)
		case images != nil && strings.HasPrefix(image, images.Destination()+"/"):
			pushed = append(pushed, image)
		}
	}
	resources.RemovePushedImages(ctx, images, app, pushed, keptNow, log)
	if len(ours) == 0 {
		return nil
	}
	removed, err := registry.Forget(ctx, api, target, ours, keptNow, time.Now().Add(-imageReclaimGrace))
	resources.SayRemovedImages(log, app, removed)
	return err
}

func otherStacksImages(store keyvalue.Store, ref provider.StackRef) registry.Recorded {
	return func(ctx context.Context) (map[string]bool, error) {
		return stackrecords.ListRecordedAppImages(ctx, store, ref.Project, ref.Name.App, ref)
	}
}

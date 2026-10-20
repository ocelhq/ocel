package assets

import (
	"context"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/grants"
)

func Grants(ctx context.Context, c *gcloud.Clients, spec provider.StackSpec, member string) ([]grants.Grant, error) {
	if !servesStaticFiles(spec) {
		return nil, nil
	}
	storage := spec.App.StorageCoordinate
	app, err := naming.AppScope(storage)
	if err != nil {
		return nil, err
	}
	buckets, err := c.TierBuckets(ctx, spec.Ref.Tier, naming.StorageEdge)
	if err != nil {
		return nil, err
	}
	condition, err := gcloud.ObjectKindsCondition("ocel assets of "+storage.Project+"/"+storage.Env+"/"+storage.App, buckets, app, naming.ObjectAssets, naming.ObjectImageConfig)
	if err != nil {
		return nil, err
	}
	return []grants.Grant{grants.OnProject(grants.AssetsRole, member, condition)}, nil
}

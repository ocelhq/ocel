package gcp

import (
	"context"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/grants"
	"github.com/ocelhq/ocel/platform/gcp/provider/resources/variables"
)

func cacheGrants(ctx context.Context, c *gcloud.Clients, spec provider.StackSpec, member string) ([]grants.Grant, error) {
	if spec.App.ISR == nil {
		return nil, nil
	}
	buckets, err := c.TierBuckets(ctx, spec.Ref.Tier, naming.StorageCache)
	if err != nil {
		return nil, err
	}
	storage := spec.App.StorageCoordinate
	app, err := naming.AppScope(storage)
	if err != nil {
		return nil, err
	}
	granted, err := grants.OnObjects("ocel cache of "+storage.Project+"/"+storage.Env+"/"+storage.App, buckets, app, grants.ObjectsRole, member, naming.ISRObjectKinds...)
	if err != nil {
		return nil, err
	}
	return append(granted, grants.OnProject(tagRecordsRole, member, variables.ProjectWritableDatabaseCondition(c, spec.Ref.Tier, spec.Ref.Project))), nil
}

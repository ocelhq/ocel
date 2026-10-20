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
	buckets, err := c.TierBuckets(ctx, spec.Ref.Tier)
	if err != nil {
		return nil, err
	}
	storage := spec.App.Storage
	app, err := naming.AppScope(storage)
	if err != nil {
		return nil, err
	}
	condition, err := gcloud.ObjectKindsCondition("ocel cache of "+storage.Project+"/"+storage.Env+"/"+storage.App, buckets, app, naming.ISRObjectKinds...)
	if err != nil {
		return nil, err
	}
	return []grants.Grant{
		grants.OnProject(grants.ObjectsRole, member, condition),
		grants.OnProject(tagRecordsRole, member, variables.ProjectWritableDatabaseCondition(c, spec.Ref.Tier, spec.Ref.Project)),
	}, nil
}

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
	release := spec.App.ISR.Coordinate
	app := naming.Coordinate{Project: release.Project, Env: release.Env, App: release.App}
	title := "ocel cache of " + app.Project + "/" + app.Env + "/" + app.App
	return []grants.Grant{
		grants.OnProject(grants.ObjectsRole, member, gcloud.ObjectKindsCondition(title, buckets, app, naming.ISRObjectKinds...)),
		grants.OnProject(tagRecordsRole, member, variables.ProjectWritableDatabaseCondition(c, spec.Ref.Tier, spec.Ref.Project)),
	}, nil
}

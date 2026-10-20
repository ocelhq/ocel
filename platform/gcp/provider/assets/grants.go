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
	keys, err := newStaticKeys(spec.App.AssetPrefix)
	if err != nil {
		return nil, err
	}
	buckets, err := c.TierBuckets(ctx, spec.Ref.Tier)
	if err != nil {
		return nil, err
	}
	app := keys.app.Project + "/" + keys.app.Env + "/" + keys.app.App
	condition := gcloud.ObjectKindsCondition("ocel assets of "+app, buckets, keys.app, naming.ObjectAssets, naming.ObjectImageConfig)
	return []grants.Grant{grants.OnProject(grants.AssetsRole, member, condition)}, nil
}

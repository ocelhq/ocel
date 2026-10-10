package gcp

import (
	"context"
	"fmt"
	"strings"

	"google.golang.org/api/cloudresourcemanager/v1"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
)

func grantAssets(ctx context.Context, c *clients, spec provider.StackSpec, account string) error {
	if !servesStaticFiles(spec) {
		return nil
	}
	keys, err := newStaticKeys(spec.App.AssetPrefix)
	if err != nil {
		return err
	}
	member := "serviceAccount:" + account
	return untilVisible(ctx, func() error {
		return c.ensureAssetsGrant(ctx, spec.Ref.Tier, member, keys)
	})
}

func (c *clients) ensureAssetsGrant(ctx context.Context, tier environment.Tier, member string, keys staticKeys) error {
	return c.bindProjectRole(ctx, member, appAssetsRole, &cloudresourcemanager.Expr{
		Title:      "ocel assets of " + strings.TrimSuffix(strings.TrimPrefix(keys.appPrefix, "assets/"), "/"),
		Expression: fmt.Sprintf("resource.name.startsWith(%q)", "projects/_/buckets/"+c.Bucket(tier)+"/objects/"+keys.appPrefix),
	}, true)
}

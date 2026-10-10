package gcp

import (
	"context"
	"strings"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
)

func grantCache(ctx context.Context, c *clients, spec provider.StackSpec, account string) error {
	if spec.App.ISR == nil {
		return nil
	}
	member := "serviceAccount:" + account
	if err := untilVisible(ctx, func() error {
		return c.ensureCacheGrant(ctx, spec.Ref.Tier, member, spec.App.ISR.Prefix)
	}); err != nil {
		return err
	}
	if keepsISRInEdgeStore(spec) {
		return nil
	}
	return untilVisible(ctx, func() error {
		return c.bindProjectRole(ctx, member, tagRecordsRole, tagDatabaseCondition(c, spec.Ref.Tier), true)
	})
}

func (c *clients) ensureCacheGrant(ctx context.Context, tier environment.Tier, member, isrPrefix string) error {
	prefix, err := cacheAppPrefix(isrPrefix)
	if err != nil {
		return err
	}
	app := strings.TrimSuffix(strings.TrimPrefix(prefix, provider.StoreCache+"/"), "/")
	return c.bindProjectRole(ctx, member, appObjectsRole, c.objectPrefixCondition("ocel cache of "+app, tier, prefix), true)
}

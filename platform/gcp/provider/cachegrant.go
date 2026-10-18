package gcp

import (
	"strings"

	"github.com/ocelhq/ocel/pkg/provider"
)

func (c *clients) cacheGrants(spec provider.StackSpec, member string) ([]appGrant, error) {
	if spec.App.ISR == nil {
		return nil, nil
	}
	prefix, err := cacheAppPrefix(spec.App.ISR.Prefix)
	if err != nil {
		return nil, err
	}
	app := strings.TrimSuffix(strings.TrimPrefix(prefix, provider.StoreCache+"/"), "/")
	return []appGrant{
		onProject(appObjectsRole, member, c.objectPrefixCondition("ocel cache of "+app, spec.Ref.Tier, prefix)),
		onProject(tagRecordsRole, member, tagDatabaseCondition(c, spec.Ref.Tier)),
	}, nil
}

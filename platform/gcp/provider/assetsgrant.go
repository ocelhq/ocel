package gcp

import (
	"strings"

	"github.com/ocelhq/ocel/pkg/provider"
)

func (c *clients) assetsGrants(spec provider.StackSpec, member string) ([]appGrant, error) {
	if !servesStaticFiles(spec) {
		return nil, nil
	}
	keys, err := newStaticKeys(spec.App.AssetPrefix)
	if err != nil {
		return nil, err
	}
	app := strings.TrimSuffix(strings.TrimPrefix(keys.appPrefix, provider.StoreAssets+"/"), "/")
	return []appGrant{onProject(appAssetsRole, member, c.objectPrefixCondition("ocel assets of "+app, spec.Ref.Tier, keys.appPrefix))}, nil
}

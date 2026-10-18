package gcp

import (
	"strings"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/grants"
)

func assetsGrants(c *gcloud.Clients, spec provider.StackSpec, member string) ([]grants.Grant, error) {
	if !servesStaticFiles(spec) {
		return nil, nil
	}
	keys, err := newStaticKeys(spec.App.AssetPrefix)
	if err != nil {
		return nil, err
	}
	app := strings.TrimSuffix(strings.TrimPrefix(keys.appPrefix, provider.StoreAssets+"/"), "/")
	return []grants.Grant{grants.OnProject(grants.AssetsRole, member, gcloud.ObjectPrefixCondition(c, "ocel assets of "+app, spec.Ref.Tier, keys.appPrefix))}, nil
}

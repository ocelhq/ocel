package gcp

import (
	"strings"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/grants"
)

func cacheGrants(c *gcloud.Clients, spec provider.StackSpec, member string) ([]grants.Grant, error) {
	if spec.App.ISR == nil {
		return nil, nil
	}
	prefix, err := cacheAppPrefix(spec.App.ISR.Prefix)
	if err != nil {
		return nil, err
	}
	app := strings.TrimSuffix(strings.TrimPrefix(prefix, provider.StoreCache+"/"), "/")
	return []grants.Grant{
		grants.OnProject(grants.ObjectsRole, member, gcloud.ObjectPrefixCondition(c, "ocel cache of "+app, spec.Ref.Tier, prefix)),
		grants.OnProject(tagRecordsRole, member, tagDatabaseCondition(c, spec.Ref.Tier)),
	}, nil
}

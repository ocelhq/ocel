package next

import (
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/grants"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
)

const cdnURLMapEnvVar = "OCEL_CDN_URL_MAP"

func purgesCloudCDN(spec provider.StackSpec) bool {
	return spec.App != nil && spec.App.ISR != nil && (spec.App.Compute != provider.ComputeContainer || spec.App.IsBuiltFromFunction()) && spec.Edge != nil && spec.Edge.Kind() == alb.Kind
}

func NewCDNPurgeEnv(names names.Names, spec provider.StackSpec) map[string]string {
	if !purgesCloudCDN(spec) {
		return nil
	}
	return map[string]string{cdnURLMapEnvVar: alb.URLMapPath(string(names.Namespace()), names.Project(), spec.Ref.Tier)}
}

func CDNPurgeGrants(c *gcloud.Clients, spec provider.StackSpec, member string) []grants.Grant {
	if !purgesCloudCDN(spec) {
		return nil
	}
	return []grants.Grant{grants.OnProject(c.CDNPurgeRolePath(), member, nil)}
}

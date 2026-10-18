package gcp

import (
	"errors"
	"strings"

	"google.golang.org/api/googleapi"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
)

const cdnURLMapEnvVar = "OCEL_CDN_URL_MAP"

func purgesCloudCDN(spec provider.StackSpec) bool {
	return spec.App != nil && spec.App.ISR != nil && spec.App.Compute != provider.ComputeContainer && spec.Edge != nil && spec.Edge.Kind() == alb.Kind
}

func newCDNPurgeEnv(names Names, spec provider.StackSpec) map[string]string {
	if !purgesCloudCDN(spec) {
		return nil
	}
	return map[string]string{cdnURLMapEnvVar: alb.URLMapPath(names.project, spec.Ref.Tier)}
}

func (c *clients) cdnPurgeGrants(spec provider.StackSpec, member string) []appGrant {
	if !purgesCloudCDN(spec) {
		return nil
	}
	return []appGrant{onProject(c.CDNPurgeRolePath(), member, nil)}
}

func isMissingRole(err error, role string) bool {
	var answered *googleapi.Error
	return errors.As(err, &answered) && answered.Code == 400 && strings.Contains(answered.Message, role)
}

func refuseMissingCDNPurgeRole(c *clients, tier environment.Tier) error {
	return refusal.Refuse(refusal.CodeNotReady,
		"the custom role %s that lets an app that revalidates behind the alb clear Cloud CDN is not in project %s: the %s bootstrap predates it.\n"+
			"Run `%s`, then deploy again", c.CDNPurgeRolePath(), c.project, tier, provider.BootstrapCommand(tier))
}

package gcp

import (
	"context"
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
	return spec.App != nil && servesNext(spec.App) && spec.App.ISR != nil && spec.Edge != nil && spec.Edge.Kind() == alb.Kind
}

func cdnPurgeEnv(names Names, spec provider.StackSpec) map[string]string {
	if !purgesCloudCDN(spec) {
		return nil
	}
	return map[string]string{cdnURLMapEnvVar: alb.URLMapPath(names.project, spec.Ref.Tier)}
}

func grantCDNPurge(ctx context.Context, c *clients, spec provider.StackSpec, account string) error {
	if !purgesCloudCDN(spec) {
		return nil
	}
	role := c.CDNPurgeRolePath()
	err := untilVisible(ctx, func() error {
		err := c.bindProjectRole(ctx, "serviceAccount:"+account, role, nil, true)
		if missingRole(err, role) {
			return refuseMissingCDNPurgeRole(c, role, spec.Ref.Tier)
		}
		return err
	})
	return explainMissingGrant(err, appGrantsGrant(c.Names))
}

func missingRole(err error, role string) bool {
	var answered *googleapi.Error
	return errors.As(err, &answered) && answered.Code == 400 && strings.Contains(answered.Message, role)
}

func refuseMissingCDNPurgeRole(c *clients, role string, tier environment.Tier) error {
	return refusal.Refuse(refusal.CodeNotReady,
		"the custom role %s that lets a Next app behind the alb clear Cloud CDN is not in project %s: the %s bootstrap predates it.\n"+
			"Run `ocel bootstrap --features alb-edge` for that tier, then deploy again", role, c.project, tier)
}

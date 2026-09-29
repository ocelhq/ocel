package cloudflare

import (
	"context"
	"fmt"
	"slices"

	cf "github.com/cloudflare/cloudflare-go/v4"
	"github.com/cloudflare/cloudflare-go/v4/cache"
)

const hostnamesPerPurge = 100

func (p *cloudflare) purgeHostnames(ctx context.Context, hostnames []string) error {
	accountID := p.accountID()
	if accountID == "" {
		return fmt.Errorf("%s is not set; it is required to purge what Cloudflare cached", envAccountID)
	}
	byZone := map[string][]string{}
	var zones []string
	for _, hostname := range hostnames {
		zoneID, _, err := p.resolveZone(ctx, accountID, routeBaseDomain(hostname))
		if err != nil {
			return err
		}
		if _, seen := byZone[zoneID]; !seen {
			zones = append(zones, zoneID)
		}
		byZone[zoneID] = append(byZone[zoneID], hostname)
	}
	for _, zoneID := range zones {
		for batch := range slices.Chunk(byZone[zoneID], hostnamesPerPurge) {
			if _, err := p.client.Cache.Purge(ctx, cache.CachePurgeParams{
				ZoneID: cf.F(zoneID),
				Body:   cache.CachePurgeParamsBodyCachePurgeFlexPurgeByHostnames{Hosts: cf.F(batch)},
			}); err != nil {
				return fmt.Errorf("purge what Cloudflare cached for %v: %w", batch, err)
			}
		}
	}
	return nil
}

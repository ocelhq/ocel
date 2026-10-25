package network

import (
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
)

func RefuseNamedNetworkMissing(ctx context.Context, c *gcloud.Clients, tier environment.Tier) error {
	engine, err := c.Compute()
	if err != nil {
		return err
	}
	project, named, subnetwork := c.Project(), c.AppNetwork(tier), c.AppSubnetwork(tier)
	_, err = googleretry.Attempted(ctx, engine.Networks.Get(project, named).Context(ctx).Do)
	if gcloud.Absent(err) {
		return refusal.Refuse(refusal.CodeInvalid,
			"option %q names the network %q, and project %s has no network by that name: make it before the deploy, or name one it has",
			"network.network", named, project)
	}
	if err != nil {
		return fmt.Errorf("read the %s network: %w", named, err)
	}
	found, err := googleretry.Attempted(ctx, engine.Subnetworks.Get(project, c.Region, subnetwork).Context(ctx).Do)
	if gcloud.Absent(err) {
		return refusal.Refuse(refusal.CodeInvalid,
			"option %q names the subnetwork %q, and project %s has none by that name in %s: make it in %s before the deploy, or name one the network has there",
			"network.subnetwork", subnetwork, project, c.Region, c.Region)
	}
	if err != nil {
		return fmt.Errorf("read the %s subnetwork: %w", subnetwork, err)
	}
	if !strings.HasSuffix(found.Network, "/"+c.AppNetworkPath(tier)) {
		return refusal.Refuse(refusal.CodeInvalid,
			"option %q names the subnetwork %q, which is on the %s network, not %q: name a subnetwork of %q",
			"network.subnetwork", subnetwork, path.Base(found.Network), named, named)
	}
	return nil
}

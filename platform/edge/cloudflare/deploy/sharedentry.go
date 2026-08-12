package cloudflare

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"

	cf "github.com/cloudflare/cloudflare-go/v4"
	"github.com/cloudflare/cloudflare-go/v4/workers"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

var _ edge.SharedEntry = (*provider)(nil)

func (p *provider) ReconcileSharedEntry(ctx context.Context, spec edge.SharedEntrySpec) error {
	accountID := os.Getenv(envAccountID)
	if accountID == "" {
		return fmt.Errorf("%s is not set; it is required to reconcile the shared preview entry worker", envAccountID)
	}
	wildcard := edge.SharedEntryWildcard(spec.BaseDomain)
	if wildcard == "" {
		return errors.New("the shared preview entry worker needs a base domain to serve")
	}

	up := upload{
		accountID:  accountID,
		scriptName: sharedEntryScriptName(spec),
		worker:     bindCodeLoader(spec.Generic),
	}
	if err := p.putWorkerScript(ctx, up, "shared preview entry worker"); err != nil {
		return err
	}
	if err := p.reconcileWorkerRoutes(ctx, up, routePlan{desired: []string{wildcard}}, spec.Warn); err != nil {
		return err
	}
	if _, err := p.setSubdomain(ctx, up, false); err != nil {
		return fmt.Errorf("set shared preview entry worker subdomain: %w", err)
	}
	return nil
}

func (p *provider) DestroySharedEntry(ctx context.Context, baseDomain string) error {
	accountID := os.Getenv(envAccountID)
	if accountID == "" {
		return fmt.Errorf("%s is not set; it is required to destroy the shared preview entry worker", envAccountID)
	}

	var errs []error
	if err := p.stripSharedEntryRoute(ctx, accountID, baseDomain); err != nil {
		errs = append(errs, err)
	}
	if err := p.detachCustomDomains(ctx, accountID, edge.SharedPreviewEntryScript); err != nil {
		errs = append(errs, err)
	}
	if err := p.deleteScript(ctx, accountID, edge.SharedPreviewEntryScript); err != nil {
		errs = append(errs, fmt.Errorf("delete worker %q: %w", edge.SharedPreviewEntryScript, err))
	}
	return errors.Join(errs...)
}

func (p *provider) stripSharedEntryRoute(ctx context.Context, accountID, baseDomain string) error {
	wildcard := edge.SharedEntryWildcard(baseDomain)
	if wildcard == "" {
		return nil
	}
	zoneID, _, err := p.resolveZone(ctx, accountID, baseDomain)
	if err != nil {
		return err
	}
	snap := p.routeSnapshot()
	inZone, err := snap.inZone(ctx, zoneID)
	if err != nil {
		return err
	}

	pattern := RoutePattern(wildcard)
	for _, route := range slices.Clone(inZone) {
		if route.Pattern != pattern || route.Script != edge.SharedPreviewEntryScript {
			continue
		}
		if _, err := p.client.Workers.Routes.Delete(ctx, route.ID, workers.RouteDeleteParams{ZoneID: cf.F(zoneID)}); err != nil {
			return fmt.Errorf("delete worker route %q: %w", pattern, err)
		}
		snap.detached(zoneID, route.ID)
	}
	return p.deleteProxiedRecord(ctx, zoneID, wildcard)
}

func sharedEntryScriptName(spec edge.SharedEntrySpec) string {
	if spec.ScriptName == "" {
		return edge.SharedPreviewEntryScript
	}
	return spec.ScriptName
}

package cloudflare

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"

	cf "github.com/cloudflare/cloudflare-go/v4"
	"github.com/cloudflare/cloudflare-go/v4/workers"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
)

const sharedEntryStem = "shared-entry"

func (n originNames) sharedPreviewEntry() string {
	return naming.OcelName(n.namespace, environment.TierPreview, n.origin.tag(), sharedEntryStem)
}

const purgeProjectParam = "project"

func (p *cloudflare) readSharedPreviewPurgeURL(ctx context.Context, accountID string, names originNames, slug string) (string, error) {
	workersDev, err := p.subdomainURL(ctx, accountID, names.sharedPreviewEntry())
	if err != nil {
		return "", fmt.Errorf("read the shared preview entry worker's subdomain: %w", err)
	}
	return workersDev + purgePath + "?" + url.Values{purgeProjectParam: {slug}}.Encode(), nil
}

func (p *cloudflare) ReconcilePreviewWildcard(ctx context.Context, spec edge.PreviewWildcardSpec) (string, error) {
	accountID, err := p.tierAccount(ctx, environment.TierPreview, "reconcile the shared preview entry worker")
	if err != nil {
		return "", err
	}
	wildcard := edge.PreviewWildcard(spec.BaseDomain)
	if wildcard == "" {
		return "", errors.New("the shared preview entry worker needs a base domain to serve")
	}

	names, err := p.names(ctx)
	if err != nil {
		return "", err
	}
	program, err := buildEntryProgram(names, environment.TierPreview, "", spec.Program)
	if err != nil {
		return "", err
	}
	if err := p.refuseBootstrapBehind(ctx, accountID, names, environment.TierPreview, bootstrapCaller{worker: "the shared preview entry worker", retry: "run this again"}); err != nil {
		return "", err
	}
	up := upload{
		accountID:  accountID,
		scriptName: program.name,
		worker:     bindCodeLoader(program.worker),
	}
	if err := p.putWorkerScript(ctx, up, "shared preview entry worker"); err != nil {
		return "", err
	}
	if err := p.reconcileWorkerRoutes(ctx, names, up, routeSpec{desired: []string{wildcard}}, spec.Warn); err != nil {
		return "", err
	}
	if _, err := p.setSubdomain(ctx, up, true); err != nil {
		return "", fmt.Errorf("set shared preview entry worker subdomain: %w", err)
	}
	return "", nil
}

func (p *cloudflare) DestroyPreviewWildcard(ctx context.Context, baseDomain string) error {
	accountID, err := p.tierAccount(ctx, environment.TierPreview, "destroy the shared preview entry worker")
	if err != nil {
		return err
	}

	names, err := p.names(ctx)
	if err != nil {
		return err
	}
	shared := names.sharedPreviewEntry()
	snap := p.routeSnapshot()
	if err := p.stripPreviewWildcardRoute(ctx, accountID, snap, shared, baseDomain); err != nil {
		return err
	}
	stillRouted, err := p.previewEntryStillRouted(ctx, accountID, snap, shared)
	if err != nil {
		return err
	}
	if stillRouted {
		return nil
	}
	if err := p.deleteScript(ctx, accountID, shared); err != nil {
		return fmt.Errorf("delete worker %q: %w", shared, err)
	}
	return nil
}

func (p *cloudflare) previewEntryStillRouted(ctx context.Context, accountID string, snap *routeSnapshot, shared string) (bool, error) {
	owned, err := p.accountZones(ctx, accountID)
	if err != nil {
		return false, err
	}
	for _, zone := range owned {
		inZone, err := snap.inZone(ctx, zone.id)
		if err != nil {
			return false, err
		}
		if slices.ContainsFunc(inZone, func(route workers.RouteListResponse) bool { return route.Script == shared }) {
			return true, nil
		}
	}
	return false, nil
}

func (p *cloudflare) stripPreviewWildcardRoute(ctx context.Context, accountID string, snap *routeSnapshot, shared, baseDomain string) error {
	wildcard := edge.PreviewWildcard(baseDomain)
	if wildcard == "" {
		return nil
	}
	zoneID, _, err := p.resolveZone(ctx, accountID, baseDomain)
	if err != nil {
		return err
	}
	inZone, err := snap.inZone(ctx, zoneID)
	if err != nil {
		return err
	}

	pattern := routePattern(wildcard)
	for _, route := range slices.Clone(inZone) {
		if route.Pattern != pattern || route.Script != shared {
			continue
		}
		if _, err := p.client.Workers.Routes.Delete(ctx, route.ID, workers.RouteDeleteParams{ZoneID: cf.F(zoneID)}); err != nil {
			return fmt.Errorf("delete worker route %q: %w", pattern, err)
		}
		snap.detached(zoneID, route.ID)
	}
	return nil
}

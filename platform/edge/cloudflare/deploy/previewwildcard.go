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

const previewEntryScript = "ocel-preview-entry"

func (p *provider) ReconcilePreviewWildcard(ctx context.Context, spec edge.PreviewWildcardSpec) error {
	accountID := os.Getenv(envAccountID)
	if accountID == "" {
		return fmt.Errorf("%s is not set; it is required to reconcile the shared preview entry worker", envAccountID)
	}
	wildcard := edge.PreviewWildcard(spec.BaseDomain)
	if wildcard == "" {
		return errors.New("the shared preview entry worker needs a base domain to serve")
	}

	if spec.Program == nil {
		return errors.New("the Cloudflare edge runs the preview entry worker; this wildcard carries no program")
	}
	up := upload{
		accountID:  accountID,
		scriptName: previewEntryScript,
		worker:     previewEntryWorker(spec),
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

func (p *provider) DestroyPreviewWildcard(ctx context.Context, baseDomain string) error {
	accountID := os.Getenv(envAccountID)
	if accountID == "" {
		return fmt.Errorf("%s is not set; it is required to destroy the shared preview entry worker", envAccountID)
	}

	var errs []error
	if err := p.stripPreviewWildcardRoute(ctx, accountID, baseDomain); err != nil {
		errs = append(errs, err)
	}
	if err := p.detachCustomDomains(ctx, accountID, previewEntryScript); err != nil {
		errs = append(errs, err)
	}
	if err := p.deleteScript(ctx, accountID, previewEntryScript); err != nil {
		errs = append(errs, fmt.Errorf("delete worker %q: %w", previewEntryScript, err))
	}
	return errors.Join(errs...)
}

func (p *provider) stripPreviewWildcardRoute(ctx context.Context, accountID, baseDomain string) error {
	wildcard := edge.PreviewWildcard(baseDomain)
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

	pattern := routePattern(wildcard)
	for _, route := range slices.Clone(inZone) {
		if route.Pattern != pattern || route.Script != previewEntryScript {
			continue
		}
		if _, err := p.client.Workers.Routes.Delete(ctx, route.ID, workers.RouteDeleteParams{ZoneID: cf.F(zoneID)}); err != nil {
			return fmt.Errorf("delete worker route %q: %w", pattern, err)
		}
		snap.detached(zoneID, route.ID)
	}
	return nil
}

func previewEntryWorker(spec edge.PreviewWildcardSpec) edge.Worker {
	worker := spec.Program.Worker
	if spec.Program.ISRWriterScriptName != "" {
		worker = withService(worker, genericISRWriterBinding, spec.Program.ISRWriterScriptName)
	}
	return bindCodeLoader(bindObjectStore(worker, spec.Values))
}

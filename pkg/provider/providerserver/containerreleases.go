package providerserver

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/router"
)

func scaleUpPromoted(ctx context.Context, hooks *provider.ContainerReleaseHooks, l projectLedger, req promoteRequest, progress progress.Log) error {
	if hooks == nil || req.rollsBackTo == "" {
		return nil
	}
	served := map[appRelease]bool{}
	for app, release := range req.promotion.Releases {
		served[appRelease{app: app, release: release}] = true
	}
	releases, err := listAppReleases(served)
	if err != nil || len(releases) == 0 {
		return err
	}
	return hooks.ScaleUp(ctx, provider.PromotedReleases{Slug: l.slug, Tier: l.tier, Env: envFor(l.tier, req.pointer), Releases: releases}, progress)
}

func scaleDownDemoted(ctx context.Context, hooks *provider.ContainerReleaseHooks, l projectLedger, req promoteRequest, kept []router.Promotion, progress progress.Log) {
	if hooks == nil {
		return
	}
	demoted, err := readDemoted(l, req, kept)
	if err == nil && (len(demoted.Previous) > 0 || len(demoted.Older) > 0) {
		err = hooks.ScaleDown(ctx, demoted, progress)
	}
	if err != nil {
		progress.Warn(fmt.Sprintf("Promotion %s serves, but scaling down the releases it demoted failed, and they keep running until the next promotion retries: %v", req.promotion.PromotionID, err))
	}
}

func readDemoted(l projectLedger, req promoteRequest, kept []router.Promotion) (provider.DemotedReleases, error) {
	previous, older := map[appRelease]bool{}, map[appRelease]bool{}
	for _, recorded := range kept {
		switch recorded.PromotionID {
		case req.promotion.PromotionID:
		case req.replaces:
			maps.Copy(previous, listDemoted(req.promotion, recorded))
		default:
			maps.Copy(older, listDemoted(req.promotion, recorded))
		}
	}
	maps.DeleteFunc(older, func(demoted appRelease, _ bool) bool { return previous[demoted] })
	listedPrevious, previousErr := listAppReleases(previous)
	listedOlder, olderErr := listAppReleases(older)
	return provider.DemotedReleases{
		Slug:     l.slug,
		Tier:     l.tier,
		Env:      envFor(l.tier, req.pointer),
		Previous: listedPrevious,
		Older:    listedOlder,
	}, errors.Join(previousErr, olderErr)
}

func listDemoted(promoted, recorded router.Promotion) map[appRelease]bool {
	demoted := map[appRelease]bool{}
	for app, release := range recorded.Releases {
		if promoted.Releases[app] != release {
			demoted[appRelease{app: app, release: release}] = true
		}
	}
	return demoted
}

func listAppReleases(set map[appRelease]bool) ([]provider.AppRelease, error) {
	keys := slices.SortedFunc(maps.Keys(set), func(a, b appRelease) int {
		return cmp.Or(cmp.Compare(a.app, b.app), cmp.Compare(a.release, b.release))
	})
	releases := make([]provider.AppRelease, 0, len(keys))
	var errs []error
	for _, key := range keys {
		release, err := provider.ParseRelease(key.release)
		if err != nil {
			errs = append(errs, fmt.Errorf("the ledger names release %q of %s, which names no stack to scale: %w", key.release, key.app, err))
			continue
		}
		releases = append(releases, provider.AppRelease{App: key.app, Release: release})
	}
	return releases, errors.Join(errs...)
}

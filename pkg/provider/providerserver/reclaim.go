package providerserver

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/ledger"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

func ReclaimPreview(ctx context.Context, p provider.Provider, slug, pointer string, removed router.PruneResult, progress progress.Progress) error {
	if err := reclaimPruned(ctx, p, slug, environment.TierPreview, pointer, removed, progress); err != nil {
		return err
	}
	return destroyPointerStacks(ctx, p, slug, pointer,
		removed.SurvivingRecordKeys, removed.SurvivingPointerRecordKeys, progress)
}

func destroyPointerStacks(ctx context.Context, p provider.Provider, slug, pointer string, surviving, servingHere []string, progress progress.Progress) error {
	entries, err := stackrecords.List(ctx, p.KeyValues(), environment.TierPreview, slug)
	if err != nil {
		return err
	}
	provisioned := make([]stackrecords.NamedStack, 0, len(entries))
	for _, entry := range entries {
		if entry.Name.Env == pointer {
			provisioned = append(provisioned, entry)
		}
	}
	slices.SortStableFunc(provisioned, func(a, b stackrecords.NamedStack) int {
		return cmp.Compare(infraLast(a.Name), infraLast(b.Name))
	})
	elsewhere, here := releasesOf(surviving), releasesOf(servingHere)

	var errs []error
	for i, entry := range provisioned {
		progress.Say(fmt.Sprintf("Destroying stack %s (%d of %d)", entry.Name, i+1, len(provisioned)))
		ref := provider.StackRef{Project: slug, Tier: environment.TierPreview, Name: entry.Name}
		if err := p.Stacks().Destroy(ctx, ref, progress); err != nil {
			errs = append(errs, fmt.Errorf("destroy %s: %w", entry.Name, err))
			continue
		}
		if err := stackrecords.Forget(ctx, p.KeyValues(), environment.TierPreview, slug, entry.Name); err != nil {
			errs = append(errs, err)
		}
		if entry.Name.IsInfra() {
			continue
		}
		for _, prefix := range reclaimedPrefixes(slug, pointer, entry.Name.App, entry.Name.Release, elsewhere, here) {
			if err := p.Artifacts().RemovePrefix(ctx, environment.TierPreview, prefix, progress); err != nil {
				errs = append(errs, fmt.Errorf("remove %s: %w", prefix, err))
			}
		}
	}
	return errors.Join(errs...)
}

func infraLast(name naming.StackName) int {
	if name.IsInfra() {
		return 1
	}
	return 0
}

type ReclaimTarget struct {
	App      string
	Build    provider.Build
	Stack    naming.StackName
	Prefixes []string
}

func ReclaimTargets(slug, env string, removed, surviving, servingHere []string) ([]ReclaimTarget, error) {
	if len(removed) == 0 {
		return nil, nil
	}
	elsewhere := releasesOf(surviving)
	here := releasesOf(servingHere)

	targets := make([]ReclaimTarget, 0, len(removed))
	for _, key := range removed {
		app, identity, ok := splitRecordKey(key)
		if !ok {
			if containerRelease(key) {
				continue
			}
			return nil, refusal.Refuse(refusal.CodeInvalid, "malformed removed record key %q, want %q", key, recordKeyPrefix+"app/identity")
		}
		release := identity.Release()
		targets = append(targets, ReclaimTarget{
			App:      app,
			Build:    identity,
			Stack:    naming.AppStack(env, app, release),
			Prefixes: reclaimedPrefixes(slug, env, app, release, elsewhere, here),
		})
	}
	return targets, nil
}

func reclaimedPrefixes(slug, env, app string, release naming.Release, elsewhere, here map[appRelease]bool) []string {
	coordinate := naming.Coordinate{Project: naming.Sanitize(slug), Env: env, App: app, Release: release}
	released := appRelease{app: app, release: release.String()}
	switch {
	case !elsewhere[released]:
		return []string{coordinate.StoragePrefix()}
	case !here[released]:
		return []string{coordinate.ISRPrefix()}
	}
	return nil
}

const recordKeyPrefix = "record:"

type appRelease struct {
	app     string
	release string
}

func containerRelease(key string) bool {
	app, identity, split := strings.Cut(strings.TrimPrefix(key, recordKeyPrefix), "/")
	if !split || app == "" {
		return false
	}
	repository, digest, pinned := strings.Cut(identity, "@")
	if !pinned || repository == "" {
		return false
	}
	hex, sha256 := strings.CutPrefix(digest, "sha256:")
	return sha256 && len(hex) == 64 && strings.IndexFunc(hex, notHex) < 0
}

func notHex(r rune) bool { return !strings.ContainsRune("0123456789abcdef", r) }

func splitRecordKey(key string) (string, provider.Build, bool) {
	app, rendered, split := strings.Cut(strings.TrimPrefix(key, recordKeyPrefix), "/")
	if !split || app == "" {
		return "", provider.Build{}, false
	}
	identity, err := provider.ParseBuild(rendered)
	if err != nil {
		return "", provider.Build{}, false
	}
	return app, identity, true
}

func releasesOf(keys []string) map[appRelease]bool {
	served := make(map[appRelease]bool, len(keys))
	for _, key := range keys {
		app, identity, ok := splitRecordKey(key)
		if !ok {
			continue
		}
		served[appRelease{app: app, release: identity.Release().String()}] = true
	}
	return served
}

func reclaimPruned(ctx context.Context, p provider.Provider, slug string, tier environment.Tier, env string, pruned router.PruneResult, progress progress.Progress) error {
	targets, err := ReclaimTargets(slug, env, pruned.RemovedRecordKeys, pruned.SurvivingRecordKeys, pruned.SurvivingPointerRecordKeys)
	if err != nil {
		return err
	}
	return destroyReclaimTargets(ctx, p, slug, tier, targets, progress)
}

func reclaimDropped(ctx context.Context, p provider.Provider, slug string, tier environment.Tier, env, promotionID string, pruned router.PruneResult, progress progress.Progress) error {
	if err := reclaimPruned(ctx, p, slug, tier, env, pruned, progress); err != nil {
		return fmt.Errorf("promotion %s serves, and the builds it dropped past the newest %d promotions were not all reclaimed: %w", promotionID, ledger.KeptPromotions, err)
	}
	return nil
}

func destroyReclaimTargets(
	ctx context.Context,
	p provider.Provider,
	slug string,
	tier environment.Tier,
	targets []ReclaimTarget,
	progress progress.Progress,
) error {
	var errs []error
	for i, target := range targets {
		progress.Say(fmt.Sprintf("Destroying the stack of %s build %s (%d of %d)", target.App, target.Build, i+1, len(targets)))
		ref := provider.StackRef{Project: slug, Tier: tier, Name: target.Stack}
		if err := p.Stacks().Destroy(ctx, ref, progress); err != nil {
			errs = append(errs, fmt.Errorf("destroy %s: %w", target.Stack, err))
			continue
		}
		if err := stackrecords.Forget(ctx, p.KeyValues(), tier, slug, target.Stack); err != nil {
			errs = append(errs, err)
		}
		for _, prefix := range target.Prefixes {
			if err := p.Artifacts().RemovePrefix(ctx, tier, prefix, progress); err != nil {
				errs = append(errs, fmt.Errorf("remove %s: %w", prefix, err))
			}
		}
	}
	return errors.Join(errs...)
}

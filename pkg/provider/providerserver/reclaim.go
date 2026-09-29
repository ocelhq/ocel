package providerserver

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/ledger"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

func ReclaimPreview(ctx context.Context, p provider.Provider, slug, pointer string, removed router.PruneResult, progress progress.Progress) error {
	if err := reclaimUnnamed(ctx, p, openProjectLedger(p, environment.TierPreview, slug), pointer, removed, progress); err != nil {
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
	restored, err := resources.RecordUnrecordedHolders(ctx, p.KeyValues(), environment.TierPreview, slug,
		func(stack naming.StackName) bool { return stack.Env == pointer })
	if err != nil {
		return err
	}
	for _, stack := range restored {
		provisioned = append(provisioned, stackrecords.NamedStack{Name: stack})
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

func ReclaimTargets(slug, env string, removed []router.DeploymentRecord, surviving, servingHere []string, containersRetained bool) ([]ReclaimTarget, []string, error) {
	if len(removed) == 0 {
		return nil, nil, nil
	}
	elsewhere := releasesOf(surviving)
	here := releasesOf(servingHere)

	targets := make([]ReclaimTarget, 0, len(removed))
	var refused []string
	var errs []error
	for _, record := range removed {
		if isRetainedContainer(containersRetained, record.Image) {
			continue
		}
		identity, err := provider.ParseBuild(record.Build)
		if err != nil {
			refused = append(refused, ledger.RecordKey(record.App, record.Build))
			errs = append(errs, refusal.Refuse(refusal.CodeInvalid, "the record of %s names no build its stack is named for, so it is kept: %s", record.App, err.Error()))
			continue
		}
		release := identity.Release()
		targets = append(targets, ReclaimTarget{
			App:      record.App,
			Build:    identity,
			Stack:    naming.AppStack(env, record.App, release),
			Prefixes: reclaimedPrefixes(slug, env, record.App, release, elsewhere, here),
		})
	}
	return targets, refused, errors.Join(errs...)
}

func isRetainedContainer(containersRetained bool, image string) bool {
	return containersRetained && image != ""
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

type appRelease struct {
	app     string
	release string
}

func releasesOf(keys []string) map[appRelease]bool {
	served := make(map[appRelease]bool, len(keys))
	for _, key := range keys {
		app, build, ok := ledger.SplitRecordKey(key)
		if !ok {
			continue
		}
		identity, err := provider.ParseBuild(build)
		if err != nil {
			continue
		}
		served[appRelease{app: app, release: identity.Release().String()}] = true
	}
	return served
}

func (s *edgeSession) reclaimDropped(ctx context.Context, pointer string, dropped []ledger.RecordedPromotion, progress progress.Progress) error {
	if len(dropped) == 0 {
		return nil
	}
	unnamed, err := s.ledger.ReadUnnamedRecords(ctx, pointer, dropped)
	if err != nil {
		return err
	}
	return reclaimUnnamed(ctx, s.provider, s.ledger, pointer, unnamed, progress)
}

func unreclaimedWarning(promotionID string, err error) string {
	return fmt.Sprintf("Promotion %s serves, but reclaiming the builds it dropped past the newest %d promotions failed, and what was not reclaimed stays until this environment is destroyed: %v",
		promotionID, ledger.KeptPromotions, err)
}

func reclaimUnnamed(ctx context.Context, p provider.Provider, l projectLedger, pointer string, unnamed router.PruneResult, progress progress.Progress) error {
	removed, err := l.ReadRecords(ctx, unnamed.UnnamedRecordKeys)
	if err != nil {
		return err
	}
	targets, refused, err := ReclaimTargets(l.slug, envFor(l.tier, pointer), removed, unnamed.SurvivingRecordKeys, unnamed.SurvivingPointerRecordKeys, p.Facts().RetainsContainerReleases)
	errs := []error{err}
	unreclaimed := map[string]bool{}
	for _, key := range refused {
		unreclaimed[key] = true
	}
	for i, target := range targets {
		progress.Say(fmt.Sprintf("Destroying the stack of %s build %s (%d of %d)", target.App, target.Build, i+1, len(targets)))
		if err := destroyReclaimTarget(ctx, p, l.slug, l.tier, target, progress); err != nil {
			errs = append(errs, err)
			unreclaimed[ledger.RecordKey(target.App, target.Build.String())] = true
		}
	}
	reclaimed := slices.DeleteFunc(slices.Clone(unnamed.UnnamedRecordKeys), func(key string) bool { return unreclaimed[key] })
	return errors.Join(append(errs, l.ForgetUnnamedRecords(ctx, reclaimed))...)
}

func destroyReclaimTarget(ctx context.Context, p provider.Provider, slug string, tier environment.Tier, target ReclaimTarget, progress progress.Progress) error {
	ref := provider.StackRef{Project: slug, Tier: tier, Name: target.Stack}
	if err := p.Stacks().Destroy(ctx, ref, progress); err != nil {
		return fmt.Errorf("destroy %s: %w", target.Stack, err)
	}
	var errs []error
	if err := stackrecords.Forget(ctx, p.KeyValues(), tier, slug, target.Stack); err != nil {
		errs = append(errs, err)
	}
	for _, prefix := range target.Prefixes {
		if err := p.Artifacts().RemovePrefix(ctx, tier, prefix, progress); err != nil {
			errs = append(errs, fmt.Errorf("remove %s: %w", prefix, err))
		}
	}
	return errors.Join(errs...)
}

func envFor(tier environment.Tier, pointer string) string {
	if tier == environment.TierProduction {
		return stackrecords.ProductionEnv
	}
	return pointer
}

const ownReclaimWindow = 15 * time.Minute

func (r *deployRun) reclaimOwnRelease(ctx context.Context) {
	if r.dry {
		return
	}
	ctx, stop := context.WithTimeout(context.WithoutCancel(ctx), ownReclaimWindow)
	defer stop()
	span := UnitSpan("reclaim/"+r.spec.PromotionID, environmentSubject(r.spec.Tier, r.spec.Env),
		"Reclaiming what this failed deploy provisioned", progressv1.Phase_PHASE_DESTROY)
	_ = r.spanEvents.run(span, func(*spanRun) error {
		progress := newProgress(r.sender, span)
		if err := r.reclaimProvisioned(ctx, progress); err != nil {
			progress.Warn(fmt.Sprintf("Promotion %s did not land, and reclaiming what its deploy provisioned failed, so what was not reclaimed stays until this environment is destroyed: %v",
				r.spec.PromotionID, err))
		}
		return nil
	})
}

func (r *deployRun) reclaimProvisioned(ctx context.Context, progress progress.Progress) error {
	retained := r.provider.Facts().RetainsContainerReleases
	var provisioned []provider.AppEntry
	for _, entry := range r.spec.Apps {
		if r.isProvisioning(entry.App) && !isRetainedContainer(retained, entry.Image) {
			provisioned = append(provisioned, entry)
		}
	}
	if len(provisioned) == 0 {
		return nil
	}
	named, err := r.ledger.ReadNamedRecordKeys(ctx)
	if err != nil {
		return err
	}
	var targets []ReclaimTarget
	for _, entry := range provisioned {
		if named[ledger.RecordKey(entry.App, entry.Build.String())] {
			continue
		}
		targets = append(targets, ReclaimTarget{
			App:      entry.App,
			Build:    entry.Build,
			Stack:    entry.Stack,
			Prefixes: []string{appCoordinate(r.spec, entry.App, entry.Build.Release()).StoragePrefix()},
		})
	}
	var errs []error
	reclaimed := make([]string, 0, len(targets))
	for i, target := range targets {
		progress.Say(fmt.Sprintf("Destroying the stack of %s build %s (%d of %d)", target.App, target.Build, i+1, len(targets)))
		if err := destroyReclaimTarget(ctx, r.provider, r.spec.Slug, r.spec.Tier, target, progress); err != nil {
			errs = append(errs, err)
			continue
		}
		reclaimed = append(reclaimed, ledger.RecordKey(target.App, target.Build.String()))
	}
	return errors.Join(append(errs, r.ledger.ForgetUnnamedRecords(ctx, reclaimed))...)
}

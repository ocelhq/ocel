package providerserver

import (
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

func ReclaimPreview(ctx context.Context, p provider.Provider, teardown provider.StackDestroy, slug, pointer string, removed router.PruneResult, progress progress.Log) error {
	if err := reclaimUnnamed(ctx, p, teardown, openProjectLedger(p, environment.TierPreview, slug), pointer, removed, progress); err != nil {
		return err
	}
	return destroyPointerStacks(ctx, p, teardown, slug, pointer,
		removed.SurvivingRecordKeys, removed.SurvivingPointerRecordKeys, progress)
}

func destroyPointerStacks(ctx context.Context, p provider.Provider, teardown provider.StackDestroy, slug, pointer string, surviving, servingHere []string, progress progress.Log) error {
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
	var apps, infra []naming.StackName
	for _, entry := range provisioned {
		if entry.Name.IsInfra() {
			infra = append(infra, entry.Name)
		} else {
			apps = append(apps, entry.Name)
		}
	}
	position := map[naming.StackName]int{}
	for i, stack := range append(slices.Clone(apps), infra...) {
		position[stack] = i + 1
	}
	elsewhere, here := releasesOf(surviving), releasesOf(servingHere)

	return errors.Join(destroyAppsThenInfra(apps, infra, func(stack naming.StackName) error {
		progress.Say(fmt.Sprintf("Destroying stack %s (%d of %d)", stack, position[stack], len(provisioned)))
		destroy := teardown
		destroy.Ref = provider.StackRef{Project: slug, Tier: environment.TierPreview, Name: stack}
		if err := p.Stacks().Destroy(ctx, destroy, progress); err != nil {
			return fmt.Errorf("destroy %s: %w", stack, err)
		}
		errs := []error{stackrecords.Forget(ctx, p.KeyValues(), environment.TierPreview, slug, stack)}
		if stack.IsInfra() {
			return errors.Join(errs...)
		}
		storage := releaseStorage(slug, pointer, stack.App, stack.Release)
		if err := removeReleaseObjects(ctx, p, environment.TierPreview, storage, reclaimedKinds(storage, elsewhere, here), progress); err != nil {
			errs = append(errs, fmt.Errorf("remove the objects of %s release %s: %w", stack.App, stack.Release, err))
		}
		return errors.Join(errs...)
	})...)
}

type ReclaimTarget struct {
	App               string
	Release           provider.Release
	Stack             naming.StackName
	StorageCoordinate naming.Coordinate
	Kinds             []naming.ObjectKind
}

func ReclaimTargets(slug, env string, removed []router.ReleaseRecord, surviving, servingHere []string, containersRetained bool) ([]ReclaimTarget, []string, error) {
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
		release, err := provider.ParseRelease(record.Release)
		if err != nil {
			refused = append(refused, ledger.RecordKey(record.App, record.Release))
			errs = append(errs, refusal.Refuse(refusal.CodeInvalid, "the record of %s names no release its stack is named for, so it is kept: %s", record.App, err.Error()))
			continue
		}
		token := release.Token()
		storage := releaseStorage(slug, env, record.App, token)
		targets = append(targets, ReclaimTarget{
			App:               record.App,
			Release:           release,
			Stack:             naming.AppStack(env, record.App, token),
			StorageCoordinate: storage,
			Kinds:             reclaimedKinds(storage, elsewhere, here),
		})
	}
	return targets, refused, errors.Join(errs...)
}

func isRetainedContainer(containersRetained bool, image string) bool {
	return containersRetained && image != ""
}

func releaseStorage(slug, env, app string, release naming.ReleaseToken) naming.Coordinate {
	return naming.Coordinate{Project: naming.Sanitize(slug), Env: env, App: app, Release: release}
}

func removeReleaseObjects(ctx context.Context, p provider.Provider, tier environment.Tier, storage naming.Coordinate, kinds []naming.ObjectKind, progress progress.Log) error {
	if len(kinds) == 0 {
		return nil
	}
	scope, err := naming.ReleaseScope(storage)
	if err != nil {
		return err
	}
	return p.Artifacts().RemoveObjects(ctx, tier, scope, kinds, progress)
}

func reclaimedKinds(storage naming.Coordinate, elsewhere, here map[appRelease]bool) []naming.ObjectKind {
	released := appRelease{app: storage.App, release: storage.Release.String()}
	switch {
	case !elsewhere[released]:
		return naming.CoordinateObjectKinds
	case !here[released]:
		return naming.ISRObjectKinds
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
		app, rendered, ok := ledger.SplitRecordKey(key)
		if !ok {
			continue
		}
		release, err := provider.ParseRelease(rendered)
		if err != nil {
			continue
		}
		served[appRelease{app: app, release: release.Token().String()}] = true
	}
	return served
}

func (s *edgeSession) reclaimDropped(ctx context.Context, teardown provider.StackDestroy, pointer string, dropped []ledger.RecordedPromotion, progress progress.Log) error {
	if len(dropped) == 0 {
		return nil
	}
	unnamed, err := s.ledger.ReadUnnamedRecords(ctx, pointer, dropped)
	if err != nil {
		return err
	}
	if err := s.removeDeployments(ctx, unnamed.DeploymentRemovals, progress); err != nil {
		return err
	}
	if err := reclaimUnnamed(ctx, s.provider, teardown, s.ledger, pointer, unnamed, progress); err != nil {
		return err
	}
	return s.forgetRemovedDeployments(ctx, pointer, unnamed.DeploymentRemovals)
}

func unreclaimedWarning(promotionID string, err error) string {
	return fmt.Sprintf("Promotion %s serves, but reclaiming the releases it dropped failed: %v", shortPromotionID(promotionID), err)
}

func reclaimUnnamed(ctx context.Context, p provider.Provider, teardown provider.StackDestroy, l projectLedger, pointer string, unnamed router.PruneResult, progress progress.Log) error {
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
	positions := make([]int, len(targets))
	for i := range positions {
		positions[i] = i
	}
	failures := destroyAppsThenInfra(positions, nil, func(i int) error {
		target := targets[i]
		progress.Say(fmt.Sprintf("Destroying the stack of %s release %s (%d of %d)", target.App, target.Release, i+1, len(targets)))
		return destroyReclaimTarget(ctx, p, teardown, l.slug, l.tier, target, progress)
	})
	for i, err := range failures {
		if err != nil {
			errs = append(errs, err)
			unreclaimed[ledger.RecordKey(targets[i].App, targets[i].Release.String())] = true
		}
	}
	reclaimed := slices.DeleteFunc(slices.Clone(unnamed.UnnamedRecordKeys), func(key string) bool { return unreclaimed[key] })
	return errors.Join(append(errs, l.ForgetUnnamedRecords(ctx, reclaimed))...)
}

func destroyReclaimTarget(ctx context.Context, p provider.Provider, teardown provider.StackDestroy, slug string, tier environment.Tier, target ReclaimTarget, progress progress.Log) error {
	teardown.Ref = provider.StackRef{Project: slug, Tier: tier, Name: target.Stack}
	if err := p.Stacks().Destroy(ctx, teardown, progress); err != nil {
		return fmt.Errorf("destroy %s: %w", target.Stack, err)
	}
	var errs []error
	if err := stackrecords.Forget(ctx, p.KeyValues(), tier, slug, target.Stack); err != nil {
		errs = append(errs, err)
	}
	if err := removeReleaseObjects(ctx, p, tier, target.StorageCoordinate, target.Kinds, progress); err != nil {
		errs = append(errs, fmt.Errorf("remove the objects of %s release %s: %w", target.App, target.Release, err))
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
	ctx, stop := context.WithTimeout(context.WithoutCancel(ctx), ownReclaimWindow)
	defer stop()
	span := RootSpan("reclaim/"+r.spec.PromotionID, environmentSubject(r.spec.Tier, r.spec.Env),
		progress.Reclaiming.Title("what this failed deploy provisioned"), progressv1.Phase_PHASE_DESTROY)
	_ = r.spanEvents.run(span, func(*spanRun) error {
		progress := newSpanLog(r.sender, span)
		if err := r.reclaimProvisioned(ctx, progress); err != nil {
			progress.Warn(fmt.Sprintf("Promotion %s did not land, and reclaiming what it provisioned failed: %v",
				shortPromotionID(r.spec.PromotionID), err))
		}
		return nil
	})
}

func (r *deployRun) reclaimProvisioned(ctx context.Context, progress progress.Log) error {
	retained := r.provider.Facts().RetainsContainerReleases
	var provisioned []provider.AppEntry
	for slot, entry := range r.spec.Apps {
		if r.provisioned[slot].provisioning && !isRetainedContainer(retained, entry.Image) {
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
		if named[ledger.RecordKey(entry.App, entry.Release.String())] {
			continue
		}
		targets = append(targets, ReclaimTarget{
			App:               entry.App,
			Release:           entry.Release,
			Stack:             entry.Stack,
			StorageCoordinate: appCoordinate(r.spec, entry.App, entry.Release.Token()),
			Kinds:             naming.CoordinateObjectKinds,
		})
	}
	teardown := r.teardown()
	teardown.KeepParts = true
	var errs []error
	reclaimed := make([]string, 0, len(targets))
	for i, target := range targets {
		progress.Say(fmt.Sprintf("Destroying the stack of %s release %s (%d of %d)", target.App, target.Release, i+1, len(targets)))
		if err := destroyReclaimTarget(ctx, r.provider, teardown, r.spec.Slug, r.spec.Tier, target, progress); err != nil {
			errs = append(errs, err)
			continue
		}
		reclaimed = append(reclaimed, ledger.RecordKey(target.App, target.Release.String()))
	}
	if len(reclaimed) > 0 {
		if err := r.noteUnusedParts(ctx); err != nil {
			progress.Warn(fmt.Sprintf("Reading which bootstrap parts no release uses after the reclaim failed: %v", err))
		}
	}
	return errors.Join(append(errs, r.ledger.ForgetUnnamedRecords(ctx, reclaimed))...)
}

func (r *deployRun) noteUnusedParts(ctx context.Context) error {
	status, err := r.readiness.Status(ctx, r.spec.Tier)
	if err != nil {
		return err
	}
	described, err := r.readiness.Bootstrap.DescribeParts(ctx, status.Description())
	if err != nil {
		return err
	}
	for _, part := range described {
		if part.RetiresWithLastUser && len(part.UsedBy) == 0 {
			r.failureNotes = append(r.failureNotes, unusedPartNote(r.spec.Tier, part.Name))
		}
	}
	return nil
}

func unusedPartNote(tier environment.Tier, part string) string {
	return fmt.Sprintf("the %s bootstrap part stays, though no release uses it: a retry reuses it, and `%s --remove %s` takes it down",
		part, provider.BootstrapCommand(tier), part)
}

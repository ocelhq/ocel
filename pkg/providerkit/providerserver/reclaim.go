package providerserver

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	"github.com/ocelhq/ocel/pkg/providerkit/stackrecords"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func ReclaimPreview(ctx context.Context, p provider.Provider, slug, pointer string, removed edge.PruneResult, progress edge.Progress) error {
	targets, err := ReclaimTargets(slug, pointer,
		removed.RemovedRecordKeys, removed.SurvivingRecordKeys, removed.SurvivingPointerRecordKeys)
	if err != nil {
		return err
	}
	if err := destroyReclaimTargets(ctx, p, slug, edge.ClassPreview, targets, progress); err != nil {
		return err
	}
	return destroyPointerStacks(ctx, p, slug, pointer,
		removed.SurvivingRecordKeys, removed.SurvivingPointerRecordKeys, progress)
}

func destroyPointerStacks(ctx context.Context, p provider.Provider, slug, pointer string, surviving, servingHere []string, progress edge.Progress) error {
	entries, err := stackrecords.List(ctx, p.Records(), edge.ClassPreview, slug)
	if err != nil {
		return err
	}
	standing := make([]stackrecords.NamedStack, 0, len(entries))
	for _, entry := range entries {
		if entry.Name.Env == pointer {
			standing = append(standing, entry)
		}
	}
	slices.SortStableFunc(standing, func(a, b stackrecords.NamedStack) int {
		return cmp.Compare(infraLast(a.Name), infraLast(b.Name))
	})
	elsewhere, here := releasesOf(surviving), releasesOf(servingHere)

	var errs []error
	for _, entry := range standing {
		progress.Say("Destroying " + entry.Name.String())
		ref := provider.StackRef{Project: slug, Class: edge.ClassPreview, Name: entry.Name}
		if err := p.Stacks().Destroy(ctx, ref, progress); err != nil {
			errs = append(errs, fmt.Errorf("destroy %s: %w", entry.Name, err))
			continue
		}
		if err := stackrecords.Forget(ctx, p.Records(), edge.ClassPreview, slug, entry.Name); err != nil {
			errs = append(errs, err)
		}
		if entry.Name.IsInfra() {
			continue
		}
		for _, prefix := range reclaimedPrefixes(slug, pointer, entry.Name.App, entry.Name.Release, elsewhere, here) {
			if err := p.Artifacts().RemovePrefix(ctx, edge.ClassPreview, prefix, progress); err != nil {
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

func destroyReclaimTargets(
	ctx context.Context,
	p provider.Provider,
	slug string,
	class edge.Class,
	targets []ReclaimTarget,
	progress edge.Progress,
) error {
	var errs []error
	for _, target := range targets {
		progress.Say("Reclaiming " + target.App + " " + target.Build.String())
		ref := provider.StackRef{Project: slug, Class: class, Name: target.Stack}
		if err := p.Stacks().Destroy(ctx, ref, progress); err != nil {
			errs = append(errs, fmt.Errorf("destroy %s: %w", target.Stack, err))
			continue
		}
		if err := stackrecords.Forget(ctx, p.Records(), class, slug, target.Stack); err != nil {
			errs = append(errs, err)
		}
		for _, prefix := range target.Prefixes {
			if err := p.Artifacts().RemovePrefix(ctx, class, prefix, progress); err != nil {
				errs = append(errs, fmt.Errorf("remove %s: %w", prefix, err))
			}
		}
	}
	return errors.Join(errs...)
}

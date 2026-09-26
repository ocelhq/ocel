package box

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/ocelhq/ocel/pkg/providerkit/appbuild"
	kitledger "github.com/ocelhq/ocel/pkg/providerkit/ledger"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
	"github.com/ocelhq/ocel/platform/vps/provider/live"
	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

type stack struct {
	e     *Edge
	state edge.StackState
}

var _ edge.EdgeStack = (*stack)(nil)

func (s *stack) State() edge.StackState { return s.state }

func (s *stack) ledger() *kitledger.Ledger {
	return kitledger.New(s.e.records, s.state.Class, s.state.Slug)
}

func (s *stack) Ledger() edge.Ledger { return s.ledger() }

func (s *stack) surface() string { return Surface(s.state.Slug, s.state.Class) }

func (s *stack) routeKey(pointer, app string) host.RouteKey {
	return host.RouteKey{Owner: s.surface(), Pointer: named(pointer), App: app}
}

func named(pointer string) string {
	if pointer == "" {
		return edge.DefaultPointer
	}
	return pointer
}

type standing struct {
	key    host.RouteKey
	app    string
	record edge.DeploymentRecord
}

func (s *stack) Promote(ctx context.Context, promotion edge.Promotion, pointer string, progress edge.Progress) error {
	ready := make([]standing, 0, len(promotion.Builds))
	for _, app := range slices.Sorted(maps.Keys(promotion.Builds)) {
		held, serves, err := s.standing(ctx, app, pointer, promotion)
		if err != nil {
			return err
		}
		if serves {
			ready = append(ready, held)
		}
	}
	if err := s.ledger().Promote(ctx, promotion, pointer, progress); err != nil {
		return err
	}
	err := s.serve(ctx, pointer, promotion, ready, progress)
	var unserved host.Unserved
	if !errors.As(err, &unserved) {
		return err
	}
	if undo := s.ledger().Unpromote(ctx, promotion.PromotionID, pointer); undo != nil {
		return errors.Join(err, fmt.Errorf("the ledger still points %s at %s, which this box never served: %w",
			named(pointer), promotion.PromotionID, undo))
	}
	return err
}

func (s *stack) serve(ctx context.Context, pointer string, promotion edge.Promotion, ready []standing, progress edge.Progress) error {
	claims, err := s.previewClaims(ctx, pointer, slices.Sorted(maps.Keys(promotion.Builds)))
	if err != nil {
		return host.Unserved{Err: err}
	}
	if err := s.claim(ctx, claims); err != nil {
		return host.Unserved{Err: err}
	}
	apps := make([]host.AppRelease, 0, len(ready))
	for _, held := range ready {
		if err := s.standUp(ctx, held, progress); err != nil {
			return host.Unserved{Err: err}
		}
		apps = append(apps, host.AppRelease{
			RouteKey:   held.key,
			Target:     held.record.Physical + ":" + appbuild.InjectedPortText,
			HealthPath: held.record.HealthPath,
		})
	}
	return s.e.machine.Release(ctx, host.Release{
		Apps:          apps,
		DeployTimeout: host.DeployWindow,
		DrainTimeout:  host.DrainWindow,
		Holding:       func(ctx context.Context) error { return s.holding(ctx, pointer, promotion.PromotionID) },
	}, progress)
}

func (s *stack) holding(ctx context.Context, pointer, promotionID string) error {
	holder, err := s.ledger().Holder(ctx, pointer)
	if err != nil {
		return err
	}
	if holder == promotionID {
		return nil
	}
	return refusal.Refuse(refusal.CodeBusy,
		"promotion %s no longer holds %s, which now names %s: another deploy moved it while this one gated, and this deploy stopped rather than flip the box onto a release the ledger no longer names. Re-run this deploy once the other one has finished if its release should serve",
		promotionID, named(pointer), holderOr(holder))
}

func holderOr(holder string) string {
	if holder == "" {
		return "nothing"
	}
	return holder
}

func (s *stack) standing(ctx context.Context, app, pointer string, promotion edge.Promotion) (standing, bool, error) {
	identity := promotion.Builds[app]
	record, found, err := s.ledger().Record(ctx, app, identity)
	if err != nil {
		return standing{}, false, err
	}
	if !found {
		return standing{}, false, refusal.Refuse(refusal.CodeInvalid,
			"promote %s: no deployment record for %s/%s\nRe-run the deploy that built it",
			promotion.PromotionID, app, identity)
	}
	if record.Physical == "" {
		return standing{}, false, nil
	}
	if record.Image == "" || record.HealthPath == "" {
		return standing{}, false, refusal.Refuse(refusal.CodeInvalid,
			"promote %s: the record for %s/%s (container %s) lacks an image (%q) or health path (%q)",
			promotion.PromotionID, app, identity, record.Physical, record.Image, record.HealthPath)
	}
	held, err := s.e.machine.HoldsImage(ctx, record.Image)
	if err != nil {
		return standing{}, false, err
	}
	if !held {
		return standing{}, false, refusal.Refuse(refusal.CodeNotReady,
			"promote %s: this box no longer holds %s for %s/%s; %s is unchanged\nDeploy again",
			promotion.PromotionID, record.Image, app, identity, app)
	}
	return standing{key: s.routeKey(pointer, app), app: app, record: record}, true, nil
}

func declaredBy(record edge.DeploymentRecord) []string {
	declared := make([]string, 0, len(record.Variables)+len(record.Env))
	for _, variable := range record.Variables {
		declared = append(declared, variable.Key)
	}
	for name := range record.Env {
		declared = append(declared, name)
	}
	slices.Sort(declared)
	return declared
}

func (s *stack) standUp(ctx context.Context, held standing, progress edge.Progress) error {
	record := held.record
	if progress != nil {
		progress.Say("Standing " + held.app + " back up as " + record.Physical)
	}
	if err := s.e.machine.StandUp(ctx, host.Container{
		Name: record.Physical, Project: s.state.Slug, App: held.app, Image: record.Image, Class: s.state.Class,
		HealthPath: record.HealthPath, Declared: declaredBy(record),
	}); err != nil {
		return err
	}
	return s.e.machine.Promote(ctx, s.state.Class, s.state.Slug, held.app, record.Image)
}

func (s *stack) previewSite() edge.PreviewSite {
	if s.state.Class != edge.ClassPreview {
		return edge.PreviewSite{}
	}
	if s.state.GlobalPreview == "" && s.state.PreviewBase != "" {
		return edge.ProjectPreview(s.state.PreviewBase)
	}
	return edge.SharedPreview(s.state.Slug, s.state.GlobalPreview)
}

func (s *stack) previewClaims(ctx context.Context, pointer string, apps []string) ([]host.HostClaim, error) {
	site := s.previewSite()
	if !site.Serves() || len(apps) == 0 || named(pointer) == edge.DefaultPointer {
		return nil, nil
	}
	stores, err := s.stores(ctx, named(pointer))
	if err != nil {
		return nil, err
	}
	hostnames := site.Hosts(pointer, apps)
	if stores {
		hostnames = append(hostnames, site.Host(pointer, switchboard.StoreLabel))
	}
	if err := site.LabelProblem(hostnames); err != nil {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"%s claims no preview hostname on this box: %s", s.surface(), err)
	}
	claims := make([]host.HostClaim, 0, len(hostnames))
	for _, hostname := range hostnames {
		app := ""
		if at := slices.IndexFunc(apps, func(app string) bool { return site.Host(pointer, app) == hostname }); at >= 0 {
			app = apps[at]
		}
		if hostname == site.Host(pointer, switchboard.StoreLabel) {
			app = switchboard.StoreLabel
		}
		claims = append(claims, host.HostClaim{
			Hostname: hostname, Owner: s.surface(), Pointer: pointer, App: app,
		})
	}
	return claims, nil
}

func (s *stack) RemovePointer(ctx context.Context, pointer string, progress edge.Progress) (edge.PruneResult, error) {
	if err := s.e.machine.DisclaimPointer(ctx, s.surface(), named(pointer)); err != nil {
		return edge.PruneResult{}, err
	}
	if err := s.holdOrigins(ctx); err != nil {
		progress.Say(s.released("preview "+pointer, err).Error())
	}
	if err := s.e.machine.UnroutePointer(ctx, s.surface(), named(pointer)); err != nil {
		return edge.PruneResult{}, err
	}
	return s.ledger().RemovePointer(ctx, pointer)
}

func (s *stack) BindDomain(ctx context.Context, binding edge.DomainBinding) error {
	if binding.Hostname == "" {
		return refusal.Refuse(refusal.CodeInvalid, "this binding names no hostname for %s to claim", s.surface())
	}
	address, err := s.e.machine.Address(ctx)
	if err != nil {
		return err
	}
	claims := []host.HostClaim{{
		Hostname: binding.Hostname, Owner: s.surface(), Pointer: edge.DefaultPointer, App: binding.App,
	}}
	stores, err := s.stores(ctx, edge.DefaultPointer)
	if err != nil {
		return err
	}
	if stores {
		claims = append(claims, host.HostClaim{
			Hostname: live.StoreHostname(binding.Hostname), Owner: s.surface(),
			Pointer: edge.DefaultPointer, App: switchboard.StoreLabel,
		})
	}
	if err := s.claim(ctx, claims); err != nil {
		return err
	}
	s.state.Bind(binding.Hostname)
	s.state.PublishFront(binding.Hostname, address)
	if route := s.e.machine.RouteBy(binding.Hostname); route != "" && binding.Say != nil {
		binding.Say(route)
	}
	return nil
}

func (s *stack) claim(ctx context.Context, claims []host.HostClaim) error {
	if len(claims) == 0 {
		return nil
	}
	if err := s.e.machine.ClaimHosts(ctx, claims); err != nil {
		return err
	}
	return s.holdOrigins(ctx)
}

func (s *stack) holdOrigins(ctx context.Context) error {
	return s.e.origins(ctx, s.state.Slug, s.state.Class)
}

func (s *stack) stores(ctx context.Context, pointer string) (bool, error) {
	upstream, err := s.e.machine.Serving(ctx, s.routeKey(pointer, switchboard.StoreLabel))
	return upstream != "", err
}

func (s *stack) UnbindDomain(ctx context.Context, hostname string) error {
	if err := s.e.machine.DisclaimHost(ctx, hostname, s.surface()); err != nil {
		return err
	}
	if err := s.e.machine.DisclaimHost(ctx, live.StoreHostname(hostname), s.surface()); err != nil {
		return err
	}
	s.state.Release(hostname)
	s.state.PublishFront(hostname, "")
	if err := s.holdOrigins(ctx); err != nil {
		return edge.Warned(s.released(hostname, err))
	}
	return nil
}

func (s *stack) released(what string, err error) error {
	return fmt.Errorf("%s is released, but this project's buckets still answer it as an origin until the next deploy holds them to what the project claims: %w", what, err)
}

func (s *stack) Destroy(ctx context.Context) error {
	var errs []error
	if err := s.e.machine.DisclaimSurface(ctx, s.surface()); err != nil {
		errs = append(errs, err)
	} else {
		for _, hostname := range slices.Clone(s.state.Bound) {
			s.state.Release(hostname)
			s.state.PublishFront(hostname, "")
		}
	}
	if err := s.e.machine.UnrouteSurface(ctx, s.surface()); err != nil {
		errs = append(errs, err)
	}
	if err := s.e.machine.ForgetNetwork(ctx, s.state.Class, s.state.Slug); err != nil {
		errs = append(errs, err)
	}
	if err := s.ledger().Destroy(ctx); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

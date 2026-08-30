package box

import (
	"context"
	"errors"
	"maps"
	"slices"

	"github.com/ocelhq/ocel/pkg/providerkit"
	kitledger "github.com/ocelhq/ocel/pkg/providerkit/ledger"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
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

func (s *stack) Promote(ctx context.Context, promotion edge.Promotion, pointer string, report edge.Reporter) error {
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
	if err := s.ledger().Promote(ctx, promotion, pointer, report); err != nil {
		return err
	}
	for _, held := range ready {
		if err := s.serve(ctx, held, report); err != nil {
			return err
		}
	}
	return nil
}

func (s *stack) standing(ctx context.Context, app, pointer string, promotion edge.Promotion) (standing, bool, error) {
	identity := promotion.Builds[app]
	record, found, err := s.ledger().Record(ctx, app, identity)
	if err != nil {
		return standing{}, false, err
	}
	if !found {
		return standing{}, false, providerkit.Refuse(providerkit.CodeInvalid,
			"promote %s: the deployments ledger holds no record for %s/%s, so nothing names what this box would put in front of %s; re-run the deploy that built it",
			promotion.PromotionID, app, identity, app)
	}
	if record.Physical == "" {
		return standing{}, false, nil
	}
	if record.Image == "" || record.HealthPath == "" {
		return standing{}, false, providerkit.Refuse(providerkit.CodeInvalid,
			"promote %s: the deployment record for %s/%s names the container %s and no image (%q) or health path (%q), and a release gates the container it re-points at on the path the wire named",
			promotion.PromotionID, app, identity, record.Physical, record.Image, record.HealthPath)
	}
	held, err := s.e.machine.HoldsImage(ctx, record.Image)
	if err != nil {
		return standing{}, false, err
	}
	if !held {
		return standing{}, false, providerkit.Refuse(providerkit.CodeNotReady,
			"promote %s: this box no longer holds %s, which %s/%s was released from, so there is nothing here to put back in front of %s and %s still serves what it served before. The box keeps the last few releases of an app and this one has fallen out of that window: deploy again",
			promotion.PromotionID, record.Image, app, identity, app, app)
	}
	return standing{key: s.routeKey(pointer, app), app: app, record: record}, true, nil
}

func (s *stack) serve(ctx context.Context, held standing, report edge.Reporter) error {
	record := held.record
	if report != nil {
		report.Say("Standing " + held.app + " back up as " + record.Physical + " from the image this host already holds")
	}
	if err := s.e.machine.StandUp(ctx, host.Container{Name: record.Physical, App: held.app, Image: record.Image}); err != nil {
		return err
	}
	if err := s.e.machine.Promote(ctx, s.state.Class, held.app, record.Image); err != nil {
		return err
	}
	retiring, err := s.e.machine.Serving(ctx, held.key)
	if err != nil {
		return err
	}
	return s.e.machine.Release(ctx, host.Release{
		RouteKey:      held.key,
		Target:        record.Physical + ":" + host.AppPort,
		Retire:        retiring,
		HealthPath:    record.HealthPath,
		DeployTimeout: host.DeployWindow,
		DrainTimeout:  host.DrainWindow,
	}, report)
}

func (s *stack) RemovePointer(ctx context.Context, pointer string) (edge.PruneResult, error) {
	if err := s.e.machine.UnroutePointer(ctx, s.surface(), named(pointer)); err != nil {
		return edge.PruneResult{}, err
	}
	return s.ledger().RemovePointer(ctx, pointer)
}

func (s *stack) BindDomain(ctx context.Context, binding edge.DomainBinding) error {
	if binding.Hostname == "" {
		return providerkit.Refuse(providerkit.CodeInvalid, "this binding names no hostname for %s to claim", s.surface())
	}
	address, err := s.e.machine.Address(ctx)
	if err != nil {
		return err
	}
	if err := s.e.machine.ClaimHost(ctx, host.HostClaim{Hostname: binding.Hostname, Owner: s.surface()}); err != nil {
		return err
	}
	s.state.Bind(binding.Hostname)
	s.state.PublishFront(binding.Hostname, address)
	return nil
}

func (s *stack) UnbindDomain(ctx context.Context, hostname string) error {
	if err := s.e.machine.DisclaimHost(ctx, hostname, s.surface()); err != nil {
		return err
	}
	s.state.Release(hostname)
	s.state.PublishFront(hostname, "")
	return nil
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
	if err := s.ledger().Destroy(ctx); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

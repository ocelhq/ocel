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
	claims, err := s.previewClaims(pointer, slices.Sorted(maps.Keys(promotion.Builds)))
	if err != nil {
		return err
	}
	if err := s.e.machine.ClaimHosts(ctx, claims); err != nil {
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

func (s *stack) previewSite() edge.PreviewSite {
	if s.state.Class != edge.ClassPreview {
		return edge.PreviewSite{}
	}
	return edge.SharedPreview(s.state.Slug, s.state.GlobalPreview)
}

func (s *stack) previewClaims(pointer string, apps []string) ([]host.HostClaim, error) {
	site := s.previewSite()
	if !site.Serves() || len(apps) == 0 || named(pointer) == edge.DefaultPointer {
		return nil, nil
	}
	hostnames := site.Hosts(pointer, apps)
	if err := site.LabelProblem(hostnames); err != nil {
		return nil, providerkit.Refuse(providerkit.CodeInvalid,
			"%s claims no preview hostname on this box: %s", s.surface(), err)
	}
	claims := make([]host.HostClaim, 0, len(hostnames))
	for _, hostname := range hostnames {
		app := ""
		if at := slices.IndexFunc(apps, func(app string) bool { return site.Host(pointer, app) == hostname }); at >= 0 {
			app = apps[at]
		}
		claims = append(claims, host.HostClaim{
			Hostname: hostname, Owner: s.surface(), Pointer: pointer, App: app,
		})
	}
	return claims, nil
}

func (s *stack) RemovePointer(ctx context.Context, pointer string, report edge.Reporter) (edge.PruneResult, error) {
	terminating, err := s.previewHostnames(ctx, named(pointer))
	if err != nil {
		return edge.PruneResult{}, err
	}
	served, err := s.served(ctx, pointer)
	if err != nil {
		return edge.PruneResult{}, err
	}
	if err := s.e.machine.DisclaimPointer(ctx, s.surface(), named(pointer)); err != nil {
		return edge.PruneResult{}, err
	}
	if err := s.e.machine.UnroutePointer(ctx, s.surface(), named(pointer)); err != nil {
		return edge.PruneResult{}, err
	}
	removed, err := s.ledger().RemovePointer(ctx, pointer)
	if err != nil {
		return edge.PruneResult{}, err
	}
	if err := s.e.machine.ForgetCertificates(ctx, terminating, report); err != nil {
		return removed, err
	}
	for _, app := range slices.Sorted(maps.Keys(served)) {
		if err := s.e.machine.Reconcile(ctx, app, served[app], report); err != nil {
			return removed, err
		}
	}
	return removed, nil
}

func (s *stack) previewHostnames(ctx context.Context, pointer string) ([]string, error) {
	if s.state.Class != edge.ClassPreview || pointer == edge.DefaultPointer {
		return nil, nil
	}
	claims, err := s.e.machine.Claims(ctx)
	if err != nil {
		return nil, err
	}
	held := make([]string, 0, len(claims))
	for _, claim := range claims {
		if claim.Owner == s.surface() && claim.Pointer == pointer {
			held = append(held, claim.Hostname)
		}
	}
	slices.Sort(held)
	return held, nil
}

func (s *stack) served(ctx context.Context, pointer string) (map[string]string, error) {
	history, err := s.ledger().History(ctx, pointer)
	if err != nil {
		return nil, err
	}
	served := map[string]string{}
	for _, entry := range history {
		for app, identity := range entry.Builds {
			if _, held := served[app]; held {
				continue
			}
			record, found, err := s.ledger().Record(ctx, app, identity)
			if err != nil {
				return nil, err
			}
			if !found || record.Image == "" {
				continue
			}
			served[app] = record.Image
		}
	}
	return served, nil
}

func (s *stack) BindDomain(ctx context.Context, binding edge.DomainBinding) error {
	if binding.Hostname == "" {
		return providerkit.Refuse(providerkit.CodeInvalid, "this binding names no hostname for %s to claim", s.surface())
	}
	address, err := s.e.machine.Address(ctx)
	if err != nil {
		return err
	}
	if err := s.e.machine.ClaimHosts(ctx, []host.HostClaim{{
		Hostname: binding.Hostname, Owner: s.surface(), Pointer: edge.DefaultPointer, App: binding.App,
	}}); err != nil {
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

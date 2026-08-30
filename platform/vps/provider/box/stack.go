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

func (s *stack) Promote(ctx context.Context, promotion edge.Promotion, pointer string, report edge.Reporter) error {
	if err := s.ledger().Promote(ctx, promotion, pointer, report); err != nil {
		return err
	}
	for _, app := range slices.Sorted(maps.Keys(promotion.Builds)) {
		if err := s.serve(ctx, app, promotion, report); err != nil {
			return err
		}
	}
	return nil
}

func (s *stack) serve(ctx context.Context, app string, promotion edge.Promotion, report edge.Reporter) error {
	identity := promotion.Builds[app]
	record, found, err := s.ledger().Record(ctx, app, identity)
	if err != nil {
		return err
	}
	if !found {
		return providerkit.Refuse(providerkit.CodeInvalid,
			"promote %s: the deployments ledger holds no record for %s/%s, so nothing names what this box would put in front of %s; re-run the deploy that built it",
			promotion.PromotionID, app, identity, app)
	}
	if record.Physical == "" {
		return nil
	}
	if record.Image == "" || record.HealthPath == "" {
		return providerkit.Refuse(providerkit.CodeInvalid,
			"promote %s: the deployment record for %s/%s names the container %s and no image (%q) or health path (%q), and a release gates the container it re-points at on the path the wire named. That record was written by an older CLI than the one serving it; deploy again",
			promotion.PromotionID, app, identity, record.Physical, record.Image, record.HealthPath)
	}
	if err := s.e.machine.StandUp(ctx, host.Container{Name: record.Physical, App: app, Image: record.Image}); err != nil {
		return err
	}
	retiring, err := s.e.machine.Serving(ctx, app)
	if err != nil {
		return err
	}
	return s.e.machine.Release(ctx, host.Release{
		App:           app,
		Target:        record.Physical + ":" + host.AppPort,
		Retire:        retiring,
		HealthPath:    record.HealthPath,
		DeployTimeout: host.DeployWindow,
		DrainTimeout:  host.DrainWindow,
	}, report)
}

func (s *stack) RemovePointer(ctx context.Context, pointer string) (edge.PruneResult, error) {
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
	if err := s.e.machine.DisclaimHost(ctx, hostname); err != nil {
		return err
	}
	s.state.Release(hostname)
	s.state.PublishFront(hostname, "")
	return nil
}

func (s *stack) Destroy(ctx context.Context) error {
	var errs []error
	for _, hostname := range slices.Clone(s.state.Bound) {
		if err := s.UnbindDomain(ctx, hostname); err != nil {
			errs = append(errs, err)
		}
	}
	if err := s.ledger().Destroy(ctx); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

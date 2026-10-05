package alb

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
)

func (s *stack) serveDeployment(ctx context.Context, move router.PointerMove, progress progress.Log) error {
	hosts := maps.Clone(s.recorded.Hosts)
	if hosts == nil {
		hosts = map[string]Host{}
	}
	var took []string
	for _, host := range move.Hosts {
		served, err := s.deploymentHost(ctx, move, host)
		if err != nil {
			return router.Unserved{Err: err}
		}
		hosts[host.Hostname] = served
		took = append(took, host.Hostname)
	}
	if len(took) == 0 {
		return nil
	}
	if err := move.RefuseInactive(ctx); err != nil {
		return err
	}
	balancer, err := s.readLoadBalancer(ctx)
	if err != nil {
		return router.Unserved{Err: err}
	}
	if balancer != s.recorded.LoadBalancer {
		s.recorded.LoadBalancer = balancer
		s.keep()
	}
	if err := s.refuseFullURLMap(ctx, took); err != nil {
		return router.Unserved{Err: err}
	}
	if err := s.raise(ctx, hosts); err != nil {
		return router.Unserved{Err: err}
	}
	for _, hostname := range took {
		if progress != nil {
			progress.Say("Routing " + hostname + " to revision tag " + hosts[hostname].Tag + " of Cloud Run service " + hosts[hostname].Service)
		}
		if err := s.reach(ctx, hosts[hostname], hostname); err != nil {
			return router.Unserved{Err: errors.Join(err, s.unrouteUnrecorded(ctx, took), s.raise(ctx, s.recorded.Hosts))}
		}
	}
	s.recorded.Hosts = hosts
	s.keep()
	return nil
}

func (s *stack) deploymentHost(ctx context.Context, move router.PointerMove, host edge.PreviewHost) (Host, error) {
	record, found := recordOf(move.Records, host.App)
	if !found {
		return Host{}, refusal.Refuse(refusal.CodeInvalid,
			"deployment hostname %s names app %q, and promotion %s records no release of it", host.Hostname, host.App, move.Promotion.PromotionID)
	}
	revision := record.Revisions[record.Physical]
	if record.Physical == "" || revision == "" {
		return Host{}, refusal.Refuse(refusal.CodeInvalid,
			"build %s of %s recorded no revision of the service it answers on, and a deployment hostname reaches the revision its deploy created: "+
				"re-deploy %s so its release records one",
			record.Build, record.App, record.App)
	}
	tag, err := s.e.deps.Pins.ReadTag(ctx, record.Physical, revision)
	if err != nil {
		return Host{}, err
	}
	return Host{
		App:     record.App,
		Service: record.Physical,
		Tag:     tag,
		Backend: backendName(s.state.Slug, s.state.Tier, host.Hostname),
		Pointer: move.Pointer,
	}, nil
}

func recordOf(records map[string]router.DeploymentRecord, app string) (router.DeploymentRecord, bool) {
	if app == "" && len(records) == 1 {
		for _, record := range records {
			return record, true
		}
	}
	for _, name := range slices.Sorted(maps.Keys(records)) {
		if strings.EqualFold(strings.TrimSpace(name), app) {
			return records[name], true
		}
	}
	return router.DeploymentRecord{}, false
}

func (s *stack) unrouteUnrecorded(ctx context.Context, hostnames []string) error {
	var errs []error
	for _, hostname := range hostnames {
		if _, recorded := s.recorded.Hosts[hostname]; recorded {
			continue
		}
		errs = append(errs, s.e.deps.Routes.Unroute(ctx, s.recorded.LoadBalancer.URLMap, hostname))
	}
	return errors.Join(errs...)
}

func (s *stack) readLoadBalancer(ctx context.Context) (LoadBalancer, error) {
	if s.recorded.LoadBalancer.provisioned() {
		return s.recorded.LoadBalancer, nil
	}
	balancing := s.e
	if s.state.Tier == environment.TierPreview {
		preview, err := s.e.recordedPreview(ctx)
		if err != nil {
			return LoadBalancer{}, err
		}
		if preview.Shielded {
			balancing = s.e.Shielded()
		}
	}
	return balancing.readProvisionedLoadBalancer(ctx, s.state.Tier)
}

const maxHostRules = 1000

func (s *stack) refuseFullURLMap(ctx context.Context, hostnames []string) error {
	var adding []string
	for _, hostname := range hostnames {
		if _, routed := s.recorded.Hosts[hostname]; !routed {
			adding = append(adding, hostname)
		}
	}
	if len(adding) == 0 {
		return nil
	}
	ruled, err := s.e.deps.Routes.CountHostRules(ctx, s.recorded.LoadBalancer.URLMap)
	if err != nil {
		return err
	}
	if ruled+len(adding) <= maxHostRules {
		return nil
	}
	return refusal.Refuse(refusal.CodeNotReady,
		"%s would each take a host rule on the url map %s, which has %d of the %d Compute allows and cannot be raised: "+
			"remove previews this tier no longer needs with `ocel destroy preview`, then promote again",
		strings.Join(adding, ", "), s.recorded.LoadBalancer.URLMap, ruled, maxHostRules)
}

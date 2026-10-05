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
	"github.com/ocelhq/ocel/platform/gcp/provider/pin"
)

func (s *stack) routePreviewHosts(ctx context.Context, move router.PointerMove, progress progress.Log) error {
	_, _, deployment := router.ParseDeploymentPointer(move.Pointer)
	hosts := maps.Clone(s.recorded.Hosts)
	if hosts == nil {
		hosts = map[string]Host{}
	}
	tags := maps.Clone(s.recorded.DeploymentTags)
	if tags == nil {
		tags = map[string]pin.Tags{}
	}
	var took []string
	replaced := map[string]pin.Tags{}
	for _, host := range move.Hosts {
		served, deploymentTags, err := s.previewHost(ctx, move, host, deployment)
		if err != nil {
			return router.Unserved{Err: err}
		}
		if hosts[host.Hostname] == served {
			continue
		}
		if held := tagsOf(hosts[host.Hostname], tags[host.Hostname]); held != nil {
			replaced[host.Hostname] = held
		}
		hosts[host.Hostname] = served
		if deploymentTags != nil {
			tags[host.Hostname] = deploymentTags
		} else {
			delete(tags, host.Hostname)
		}
		took = append(took, host.Hostname)
	}
	withdrawn := s.listRecordedPreviewHosts(edge.ListPreviewHostnames(move.ListHostsToWithdraw()))
	if len(took) == 0 && len(withdrawn) == 0 {
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
	if err := s.refuseExhaustedLimits(ctx, took, withdrawn); err != nil {
		return router.Unserved{Err: err}
	}
	for i, hostname := range withdrawn {
		if err := s.e.deps.Routes.Unroute(ctx, s.recorded.LoadBalancer.URLMap, hostname); err != nil {
			return router.Unserved{Err: errors.Join(err, s.restoreRecordedRoutes(ctx, nil, withdrawn[:i]))}
		}
		delete(hosts, hostname)
		delete(tags, hostname)
	}
	if err := s.raise(ctx, hosts); err != nil {
		return router.Unserved{Err: errors.Join(err, s.restoreRecordedRoutes(ctx, nil, withdrawn))}
	}
	for _, hostname := range took {
		if progress != nil {
			progress.Say("Routing " + hostname + " to " + describeHost(hosts[hostname]))
		}
		if err := s.reach(ctx, hosts[hostname], hostname); err != nil {
			return router.Unserved{Err: errors.Join(err, s.restoreRecordedRoutes(ctx, took, withdrawn))}
		}
	}
	for _, hostname := range slices.Sorted(maps.Keys(replaced)) {
		s.owe(replaced[hostname])
	}
	s.recordPreviewHosts(hosts, tags)
	return nil
}

func describeHost(host Host) string {
	if host.Tag == "" {
		return host.App + "'s Cloud Run service " + host.Service
	}
	return "revision tag " + host.Tag + " of Cloud Run service " + host.Service
}

func (s *stack) previewHost(ctx context.Context, move router.PointerMove, host edge.PreviewHost, deployment bool) (Host, pin.Tags, error) {
	record, found := recordOf(move.Records, host.App)
	if !found {
		return Host{}, nil, refusal.Refuse(refusal.CodeInvalid,
			"preview hostname %s names app %q, and promotion %s records no release of it", host.Hostname, host.App, move.Promotion.PromotionID)
	}
	if record.Physical == "" {
		return Host{}, nil, refusal.Refuse(refusal.CodeInvalid,
			"build %s of %s recorded no Cloud Run service it answers on, so %s has nothing to route to: re-deploy %s so its release records one",
			record.Build, record.App, host.Hostname, record.App)
	}
	served := Host{
		App:     record.App,
		Service: record.Physical,
		Backend: backendName(s.state.Slug, s.state.Tier, host.Hostname),
		Pointer: move.Pointer,
	}
	if !deployment {
		return served, nil, nil
	}
	tags, err := pin.ReadTags(ctx, s.e.deps.Pins, record)
	if err != nil {
		return Host{}, nil, err
	}
	served.Tag = tags[record.Physical]
	return served, tags, nil
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

func (s *stack) restoreRecordedRoutes(ctx context.Context, took, withdrawn []string) error {
	return errors.Join(
		s.unrouteUnrecorded(ctx, took),
		s.raise(ctx, s.recorded.Hosts),
		s.routeRecorded(ctx, slices.Concat(took, withdrawn)),
	)
}

func (s *stack) routeRecorded(ctx context.Context, hostnames []string) error {
	var errs []error
	for _, hostname := range hostnames {
		if host, recorded := s.recorded.Hosts[hostname]; recorded {
			errs = append(errs, s.reach(ctx, host, hostname))
		}
	}
	return errors.Join(errs...)
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

func (s *stack) refuseExhaustedLimits(ctx context.Context, hostnames, freeing []string) error {
	var adding []string
	for _, hostname := range hostnames {
		if _, routed := s.recorded.Hosts[hostname]; !routed {
			adding = append(adding, hostname)
		}
	}
	if len(adding) == 0 {
		return nil
	}
	var freed int
	for _, hostname := range freeing {
		if _, routed := s.recorded.Hosts[hostname]; routed {
			freed++
		}
	}
	if err := s.refuseFullURLMap(ctx, adding, freed); err != nil {
		return err
	}
	return s.refuseExhaustedBackendServiceQuota(ctx, adding)
}

func (s *stack) refuseExhaustedBackendServiceQuota(ctx context.Context, adding []string) error {
	quota, found, err := s.e.deps.Routes.ReadBackendServiceQuota(ctx)
	if err != nil || !found {
		return err
	}
	if quota.Usage+float64(len(adding)) <= quota.Limit {
		return nil
	}
	return refusal.Refuse(refusal.CodeNotReady,
		"%s would each take a backend service, and project %s has %.0f of the %.0f its GLOBAL_EXTERNAL_MANAGED_BACKEND_SERVICES quota allows: "+
			"remove previews this tier no longer needs with `ocel destroy preview`, or ask Google Cloud to raise the quota, then promote again",
		strings.Join(adding, ", "), s.e.deps.Project, quota.Usage, quota.Limit)
}

func (s *stack) refuseFullURLMap(ctx context.Context, adding []string, freed int) error {
	ruled, err := s.e.deps.Routes.CountHostRules(ctx, s.recorded.LoadBalancer.URLMap)
	if err != nil {
		return err
	}
	if ruled+len(adding)-freed <= maxHostRules {
		return nil
	}
	return refusal.Refuse(refusal.CodeNotReady,
		"%s would each take a host rule on the url map %s, which has %d of the %d Compute allows and cannot be raised: "+
			"remove previews this tier no longer needs with `ocel destroy preview`, then promote again",
		strings.Join(adding, ", "), s.recorded.LoadBalancer.URLMap, ruled, maxHostRules)
}

func (s *stack) withdrawPointer(ctx context.Context, removal router.PointerRemoval) error {
	going := s.listPointerHosts(removal)
	if len(going) == 0 {
		return nil
	}
	hosts := maps.Clone(s.recorded.Hosts)
	for _, hostname := range going {
		if err := s.e.deps.Routes.Unroute(ctx, s.recorded.LoadBalancer.URLMap, hostname); err != nil {
			return err
		}
		delete(hosts, hostname)
	}
	if err := s.raise(ctx, hosts); err != nil {
		return err
	}
	withdrawn := s.recorded.Hosts
	recorded := s.recorded.DeploymentTags
	kept := maps.Clone(recorded)
	for _, hostname := range going {
		delete(kept, hostname)
	}
	for _, hostname := range going {
		s.owe(tagsOf(withdrawn[hostname], recorded[hostname]))
	}
	s.recordPreviewHosts(hosts, kept)
	return nil
}

func (s *stack) recordPreviewHosts(hosts map[string]Host, tags map[string]pin.Tags) {
	if len(hosts) == 0 {
		hosts = nil
	}
	if len(tags) == 0 {
		tags = nil
	}
	s.recorded.Hosts = hosts
	s.recorded.DeploymentTags = tags
	s.keep()
}

func (s *stack) listPointerHosts(removal router.PointerRemoval) []string {
	named := edge.ListPreviewHostnames(removal.Hosts)
	var going []string
	for _, hostname := range slices.Sorted(maps.Keys(s.recorded.Hosts)) {
		host := s.recorded.Hosts[hostname]
		if host.Pointer != "" && (host.Pointer == removal.Pointer || slices.Contains(named, hostname)) {
			going = append(going, hostname)
		}
	}
	return going
}

func (s *stack) listRecordedPreviewHosts(hostnames []string) []string {
	var recorded []string
	for _, hostname := range hostnames {
		if s.recorded.Hosts[hostname].Pointer != "" {
			recorded = append(recorded, hostname)
		}
	}
	return recorded
}

func tagsOf(gone Host, recorded pin.Tags) pin.Tags {
	if len(recorded) > 0 {
		return recorded
	}
	if gone.Tag == "" {
		return nil
	}
	return pin.Tags{gone.Service: gone.Tag}
}

func (s *stack) owe(gone pin.Tags) {
	for _, service := range slices.Sorted(maps.Keys(gone)) {
		owed := RevisionTag{Service: service, Tag: gone[service]}
		if !slices.Contains(s.recorded.TagsToRemove, owed) {
			s.recorded.TagsToRemove = append(s.recorded.TagsToRemove, owed)
		}
	}
}

func (s *stack) removeUnheldTags(ctx context.Context) error {
	var kept []RevisionTag
	var errs []error
	for _, owed := range s.recorded.TagsToRemove {
		if isTagHeld(s.recorded.Hosts, s.recorded.DeploymentTags, owed.Service, owed.Tag) {
			continue
		}
		if err := s.e.deps.Pins.Untag(ctx, owed.Service, owed.Tag); err != nil {
			kept = append(kept, owed)
			errs = append(errs, err)
		}
	}
	if len(s.recorded.TagsToRemove) == 0 {
		return nil
	}
	s.recorded.TagsToRemove = kept
	s.keep()
	return errors.Join(errs...)
}

func isTagHeld(hosts map[string]Host, tags map[string]pin.Tags, service, tag string) bool {
	for _, host := range hosts {
		if host.Service == service && host.Tag == tag {
			return true
		}
	}
	for _, held := range tags {
		if held[service] == tag {
			return true
		}
	}
	return false
}

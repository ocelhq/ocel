package alb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider/ledger"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/platform/gcp/provider/pin"
)

type stack struct {
	e        *Edge
	state    edge.StackState
	recorded edgeRecord
}

func (s *stack) State() edge.StackState { return s.state }

func (s *stack) openLedger() *ledger.Ledger {
	return ledgerFor(s.e.deps.KeyValues, s.state.Tier, s.state.Slug)
}

func (s *stack) released(ctx context.Context, records map[string]router.DeploymentRecord, progress progress.Log) error {
	hosts := maps.Clone(s.recorded.Hosts)
	var took []string
	for _, hostname := range slices.Sorted(maps.Keys(hosts)) {
		host := hosts[hostname]
		if host.Service != "" || isWildcard(hostname) {
			continue
		}
		service := pin.ResolveService(records[host.App])
		if service == "" {
			continue
		}
		host.Service = service
		hosts[hostname] = host
		took = append(took, hostname)
	}
	if len(took) == 0 {
		return nil
	}
	if err := s.raise(ctx, hosts); err != nil {
		return err
	}
	for _, hostname := range took {
		if progress != nil {
			progress.Say("Routing " + hostname + " to " + hosts[hostname].App + "'s Cloud Run service " + hosts[hostname].Service)
		}
		if err := s.reach(ctx, hosts[hostname], hostname); err != nil {
			return errors.Join(err, s.raise(ctx, s.recorded.Hosts))
		}
	}
	s.recorded.Hosts = hosts
	s.keep()
	return nil
}

func (s *stack) adopt(balancer LoadBalancer) error {
	if err := s.state.Private.Into(&s.recorded); err != nil {
		return err
	}
	s.recorded.LoadBalancer = balancer
	s.keep()
	return nil
}

func (s *stack) keep() { s.state.Private = edge.Own(s.recorded) }

func (s *stack) reach(ctx context.Context, host Host, hostname string) error {
	if host.Service == "" {
		return s.e.deps.Routes.ServeNotFound(ctx, s.recorded.LoadBalancer.URLMap, hostname)
	}
	return s.e.deps.Routes.Route(ctx, s.recorded.LoadBalancer.URLMap, hostname, host.Backend)
}

func (s *stack) BindDomain(ctx context.Context, binding edge.DomainBinding) error {
	if binding.Hostname == "" {
		return refusal.Refuse(refusal.CodeInvalid, "the %q edge is asked to bind a hostname nothing named", Kind)
	}
	if !s.recorded.LoadBalancer.provisioned() {
		return refusal.Refuse(refusal.CodeNotReady,
			"no %s load balancer is provisioned for tier %s, and %s is served by writing a host rule into its url map: run `ocel bootstrap` for this tier first",
			Kind, s.state.Tier, binding.Hostname)
	}
	service, err := s.serving(ctx, binding.App)
	if err != nil {
		return err
	}
	if isWildcard(binding.Hostname) {
		service = ""
	}
	if !isWildcard(binding.Hostname) {
		if err := s.refuseExhaustedLimits(ctx, []string{binding.Hostname}, nil); err != nil {
			return err
		}
	}
	hosts := maps.Clone(s.recorded.Hosts)
	if hosts == nil {
		hosts = map[string]Host{}
	}
	hosts[binding.Hostname] = Host{
		App:         binding.App,
		Certificate: binding.Certificate,
		Service:     service,
		Backend:     backendName(s.state.Slug, s.state.Tier, binding.Hostname),
	}
	claimed, err := s.claim(ctx, binding.Hostname)
	if err != nil {
		return err
	}
	release := func() error {
		if !claimed {
			return nil
		}
		return s.disown(ctx, binding.Hostname)
	}
	if err := s.raise(ctx, hosts); err != nil {
		return errors.Join(err, release())
	}
	if !isWildcard(binding.Hostname) {
		if err := s.reach(ctx, hosts[binding.Hostname], binding.Hostname); err != nil {
			return errors.Join(err, s.raise(ctx, s.recorded.Hosts), release())
		}
	}
	s.recorded.Hosts = hosts
	s.keep()
	s.state.Bind(binding.Hostname)
	s.state.PublishAddress(binding.Hostname, s.recorded.LoadBalancer.Address)
	return nil
}

func (s *stack) UnbindDomain(ctx context.Context, hostname string) error {
	if _, bound := s.recorded.Hosts[hostname]; !bound {
		s.state.Release(hostname)
		s.state.PublishAddress(hostname, "")
		return nil
	}
	hosts := maps.Clone(s.recorded.Hosts)
	delete(hosts, hostname)
	if err := s.e.deps.Routes.Unroute(ctx, s.recorded.LoadBalancer.URLMap, hostname); err != nil {
		return err
	}
	if err := s.raise(ctx, hosts); err != nil {
		return err
	}
	if err := s.disown(ctx, hostname); err != nil {
		return err
	}
	if len(hosts) == 0 {
		hosts = nil
	}
	s.recorded.Hosts = hosts
	s.keep()
	s.state.Release(hostname)
	s.state.PublishAddress(hostname, "")
	return nil
}

func (s *stack) target() Target { return Target{Tier: s.state.Tier, Slug: s.state.Slug} }

func (s *stack) raise(ctx context.Context, hosts map[string]Host) error {
	target := s.target()
	if len(hosts) == 0 {
		return s.e.deps.Stacks.Destroy(ctx, target, progress.Discard())
	}
	_, err := s.e.deps.Stacks.Up(ctx, target, bindingProgram(bindingSpec{
		Region:         s.e.deps.Region,
		Slug:           s.state.Slug,
		Tier:           s.state.Tier,
		CertificateMap: s.recorded.LoadBalancer.CertificateMap,
		Hosts:          hosts,
		Shielded:       s.recorded.LoadBalancer.Shielded,
	}), progress.Discard())
	return err
}

func (s *stack) serving(ctx context.Context, app string) (string, error) {
	if app == "" {
		return "", nil
	}
	active, found, err := s.openLedger().ReadActive(ctx, "")
	if err != nil || !found {
		return "", err
	}
	identity, released := active.Builds[app]
	if !released {
		return "", nil
	}
	record, staged, err := s.openLedger().Record(ctx, app, identity)
	if err != nil || !staged {
		return "", err
	}
	return pin.ResolveService(record), nil
}

func (s *stack) claim(ctx context.Context, hostname string) (bool, error) {
	name := s.e.claim(s.state.Tier, hostname)
	entry, err := keyvalue.ReadOrEmpty(ctx, s.e.deps.KeyValues, name)
	if err != nil {
		return false, fmt.Errorf("read what serves %s on the %s edge: %w", hostname, Kind, err)
	}
	owner := Surface(s.state.Slug, s.state.Tier)
	if len(entry.Value) > 0 {
		var existing claim
		if err := json.Unmarshal(entry.Value, &existing); err != nil {
			return false, fmt.Errorf("decode what serves %s on the %s edge: %w", hostname, Kind, err)
		}
		if existing.Owner == owner {
			return false, nil
		}
		return false, claimedBy(hostname, existing.Owner)
	}
	encoded, err := json.Marshal(claim{Owner: owner})
	if err != nil {
		return false, fmt.Errorf("encode what serves %s on the %s edge: %w", hostname, Kind, err)
	}
	entry.Value = encoded
	_, err = s.e.deps.KeyValues.Write(ctx, entry)
	if errors.Is(err, keyvalue.ErrStale) {
		return false, s.claimedMeanwhile(ctx, hostname, name)
	}
	if err != nil {
		return false, fmt.Errorf("record what serves %s on the %s edge: %w", hostname, Kind, err)
	}
	return true, nil
}

func (s *stack) claimedMeanwhile(ctx context.Context, hostname string, name keyvalue.Key) error {
	entry, err := keyvalue.ReadOrEmpty(ctx, s.e.deps.KeyValues, name)
	if err != nil || len(entry.Value) == 0 {
		return refusal.Refuse(refusal.CodeBusy,
			"%s was claimed on the %s edge while this bind was claiming it: bind it again once the other run has finished", hostname, Kind)
	}
	var existing claim
	if err := json.Unmarshal(entry.Value, &existing); err != nil {
		return fmt.Errorf("decode what serves %s on the %s edge: %w", hostname, Kind, err)
	}
	return claimedBy(hostname, existing.Owner)
}

func claimedBy(hostname, owner string) error {
	return refusal.Refuse(refusal.CodeInvalid,
		"%s is served by %s on the %s edge, and one hostname is routed to one project: release it there with `ocel domain remove` first",
		hostname, owner, Kind)
}

func (s *stack) disown(ctx context.Context, hostname string) error {
	if err := keyvalue.Forget(ctx, s.e.deps.KeyValues, s.e.claim(s.state.Tier, hostname)); err != nil {
		return fmt.Errorf("release what served %s on the %s edge: %w", hostname, Kind, err)
	}
	return nil
}

func (s *stack) Destroy(ctx context.Context) error {
	for _, hostname := range slices.Sorted(maps.Keys(s.recorded.Hosts)) {
		if err := s.e.deps.Routes.Unroute(ctx, s.recorded.LoadBalancer.URLMap, hostname); err != nil {
			return err
		}
		if err := s.disown(ctx, hostname); err != nil {
			return err
		}
	}
	if len(s.recorded.Hosts) > 0 {
		if err := s.e.deps.Stacks.Destroy(ctx, s.target(), progress.Discard()); err != nil {
			return err
		}
	}
	s.recorded.Hosts = nil
	s.recorded.Served = nil
	s.keep()
	for _, hostname := range s.state.Bound {
		s.state.PublishAddress(hostname, "")
	}
	s.state.Bound = nil
	return nil
}

const maxResourceName = 63

func resourceName(slug string, tier environment.Tier, hostname string, role ...string) string {
	segments := []naming.Segment{
		naming.Fixed("ocel"),
		naming.Fixed(string(Kind)),
		naming.Compressible(slug),
		naming.Fixed(string(tier)),
		naming.Compressible(naming.SanitizeHost(hostname)),
	}
	for _, each := range role {
		segments = append(segments, naming.Fixed(each))
	}
	return naming.Fit(maxResourceName, naming.WordSeparator, segments...)
}

func backendName(slug string, tier environment.Tier, hostname string) string {
	return resourceName(slug, tier, hostname)
}

func entryName(slug string, tier environment.Tier, hostname string) string {
	return resourceName(slug, tier, hostname, "cert")
}

func negName(slug string, tier environment.Tier, hostname string) string {
	return resourceName(slug, tier, hostname, "neg")
}

func isWildcard(hostname string) bool { return strings.HasPrefix(hostname, "*.") }

func (s *stack) warmThroughLoadBalancer(ctx context.Context, service, revision, path string) error {
	if s.e.deps.WarmURL == nil {
		return nil
	}
	hostname := s.findHostnameServing(service)
	if hostname == "" {
		return nil
	}
	address := s.recorded.LoadBalancer.Address
	if s.recorded.LoadBalancer.Shielded {
		address = ""
	}
	if err := s.e.deps.WarmURL(ctx, "https://"+hostname+path, address); err != nil {
		return fmt.Errorf("revision %s of %s: %w", revision, service, err)
	}
	return nil
}

func (s *stack) findHostnameServing(service string) string {
	for _, hostname := range slices.Sorted(maps.Keys(s.recorded.Hosts)) {
		host := s.recorded.Hosts[hostname]
		if host.Service == service && host.Tag == "" && !isWildcard(hostname) {
			return hostname
		}
	}
	return ""
}

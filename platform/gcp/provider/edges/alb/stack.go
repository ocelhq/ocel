package alb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit"
	kitledger "github.com/ocelhq/ocel/pkg/providerkit/ledger"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/gcp/provider/pin"
)

type stack struct {
	e     *Edge
	state edge.StackState
	held  held
}

func (s *stack) State() edge.StackState { return s.state }

func (s *stack) ledger() *kitledger.Ledger {
	return ledgerFor(s.e.deps.Records, s.state.Class, s.state.Slug)
}

func (s *stack) Ledger() edge.Ledger { return s.ledger() }

func (s *stack) Promote(ctx context.Context, promotion edge.Promotion, pointer string, report edge.Reporter) error {
	if err := pin.Promote(ctx, s.ledger(), s.e.deps.Pins, promotion, pointer, report); err != nil {
		return err
	}
	return s.released(ctx, promotion, report)
}

func (s *stack) released(ctx context.Context, promotion edge.Promotion, report edge.Reporter) error {
	hosts := maps.Clone(s.held.Hosts)
	serving := map[string]string{}
	var took []string
	for _, hostname := range slices.Sorted(maps.Keys(hosts)) {
		host := hosts[hostname]
		if host.Service != "" {
			continue
		}
		if _, promoted := promotion.Builds[host.App]; !promoted {
			continue
		}
		service, held := serving[host.App]
		if !held {
			found, err := s.serving(ctx, host.App)
			if err != nil {
				return err
			}
			serving[host.App], service = found, found
		}
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
		if report != nil {
			report.Detail("Routing " + hostname + " to " + hosts[hostname].Service)
		}
		if err := s.reach(ctx, hosts[hostname], hostname); err != nil {
			return errors.Join(err, s.raise(ctx, s.held.Hosts))
		}
	}
	s.held.Hosts = hosts
	s.keep()
	return nil
}

func (s *stack) RemovePointer(ctx context.Context, pointer string, _ edge.Reporter) (edge.PruneResult, error) {
	return s.ledger().RemovePointer(ctx, pointer)
}

func (s *stack) adopt(front Front) error {
	if err := s.state.Adapter.Into(&s.held); err != nil {
		return err
	}
	s.held.Front = front
	s.keep()
	return nil
}

func (s *stack) keep() { s.state.Adapter = edge.Own(s.held) }

func (s *stack) reach(ctx context.Context, host Host, hostname string) error {
	if host.Service == "" {
		return s.e.deps.Routes.Hold(ctx, s.held.Front.URLMap, hostname)
	}
	return s.e.deps.Routes.Route(ctx, s.held.Front.URLMap, hostname, host.Backend)
}

func (s *stack) BindDomain(ctx context.Context, binding edge.DomainBinding) error {
	if binding.Hostname == "" {
		return providerkit.Refuse(providerkit.CodeInvalid, "the %q edge is asked to bind a hostname nothing named", Kind)
	}
	if !s.held.Front.standing() {
		return providerkit.Refuse(providerkit.CodeNotReady,
			"no %s load balancer stands for class %s, and %s is served by writing a host rule into its url map: run `ocel bootstrap` for this class first",
			Kind, s.state.Class, binding.Hostname)
	}
	service, err := s.serving(ctx, binding.App)
	if err != nil {
		return err
	}
	hosts := maps.Clone(s.held.Hosts)
	if hosts == nil {
		hosts = map[string]Host{}
	}
	hosts[binding.Hostname] = Host{
		App:         binding.App,
		Certificate: binding.Certificate,
		Service:     service,
		Backend:     backendName(s.state.Slug, s.state.Class, binding.Hostname),
	}
	if err := s.raise(ctx, hosts); err != nil {
		return err
	}
	if err := s.reach(ctx, hosts[binding.Hostname], binding.Hostname); err != nil {
		return errors.Join(err, s.raise(ctx, s.held.Hosts))
	}
	if err := s.claim(ctx, binding.Hostname); err != nil {
		return err
	}
	s.held.Hosts = hosts
	s.keep()
	s.state.Bind(binding.Hostname)
	s.state.PublishFront(binding.Hostname, s.held.Front.Address)
	return nil
}

func (s *stack) UnbindDomain(ctx context.Context, hostname string) error {
	if _, bound := s.held.Hosts[hostname]; !bound {
		s.state.Release(hostname)
		s.state.PublishFront(hostname, "")
		return nil
	}
	hosts := maps.Clone(s.held.Hosts)
	delete(hosts, hostname)
	if err := s.e.deps.Routes.Unroute(ctx, s.held.Front.URLMap, hostname); err != nil {
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
	s.held.Hosts = hosts
	s.keep()
	s.state.Release(hostname)
	s.state.PublishFront(hostname, "")
	return nil
}

func (s *stack) target() Target { return Target{Class: s.state.Class, Slug: s.state.Slug} }

func (s *stack) raise(ctx context.Context, hosts map[string]Host) error {
	target := s.target()
	if len(hosts) == 0 {
		return s.e.deps.Stacks.Destroy(ctx, target, edge.DiscardReporter())
	}
	_, err := s.e.deps.Stacks.Up(ctx, target, bindingProgram(bindingSpec{
		Project:        s.e.deps.Project,
		Region:         s.e.deps.Region,
		Slug:           s.state.Slug,
		Class:          s.state.Class,
		CertificateMap: s.held.Front.CertificateMap,
		Hosts:          hosts,
	}), edge.DiscardReporter())
	return err
}

func (s *stack) serving(ctx context.Context, app string) (string, error) {
	if app == "" {
		return "", nil
	}
	history, err := s.ledger().History(ctx, "")
	if err != nil {
		return "", err
	}
	at := slices.IndexFunc(history, func(entry edge.HistoryEntry) bool { return entry.Active })
	if at < 0 {
		return "", nil
	}
	identity, released := history[at].Builds[app]
	if !released {
		return "", nil
	}
	record, staged, err := s.ledger().Record(ctx, app, identity)
	if err != nil || !staged {
		return "", err
	}
	return record.Physical, nil
}

func (s *stack) claim(ctx context.Context, hostname string) error {
	name := s.e.claim(s.state.Class, hostname)
	record, err := providerkit.Held(ctx, s.e.deps.Records, name)
	if err != nil {
		return fmt.Errorf("read what serves %s on the %s edge: %w", hostname, Kind, err)
	}
	encoded, err := json.Marshal(claim{Owner: Surface(s.state.Slug, s.state.Class)})
	if err != nil {
		return fmt.Errorf("encode what serves %s on the %s edge: %w", hostname, Kind, err)
	}
	record.Bytes = encoded
	if _, err := s.e.deps.Records.Write(ctx, record); err != nil {
		return fmt.Errorf("record what serves %s on the %s edge: %w", hostname, Kind, err)
	}
	return nil
}

func (s *stack) disown(ctx context.Context, hostname string) error {
	if err := providerkit.Forget(ctx, s.e.deps.Records, s.e.claim(s.state.Class, hostname)); err != nil {
		return fmt.Errorf("release what served %s on the %s edge: %w", hostname, Kind, err)
	}
	return nil
}

func (s *stack) Destroy(ctx context.Context) error {
	for _, hostname := range slices.Sorted(maps.Keys(s.held.Hosts)) {
		if err := s.e.deps.Routes.Unroute(ctx, s.held.Front.URLMap, hostname); err != nil {
			return err
		}
		if err := s.disown(ctx, hostname); err != nil {
			return err
		}
	}
	if len(s.held.Hosts) > 0 {
		if err := s.e.deps.Stacks.Destroy(ctx, s.target(), edge.DiscardReporter()); err != nil {
			return err
		}
	}
	if err := s.ledger().Destroy(ctx); err != nil {
		return err
	}
	s.held.Hosts = nil
	s.keep()
	for _, hostname := range s.state.Bound {
		s.state.PublishFront(hostname, "")
	}
	s.state.Bound = nil
	return nil
}

const maxResourceName = 63

func resourceName(slug string, class edge.Class, hostname string, role ...string) string {
	segments := []naming.Segment{
		naming.Fixed("ocel"),
		naming.Fixed(string(Kind)),
		naming.Compressible(slug),
		naming.Fixed(string(class)),
		naming.Compressible(naming.SanitizeHost(hostname)),
	}
	for _, each := range role {
		segments = append(segments, naming.Fixed(each))
	}
	return naming.Fit(maxResourceName, naming.WordSeparator, segments...)
}

func backendName(slug string, class edge.Class, hostname string) string {
	return resourceName(slug, class, hostname)
}

func entryName(slug string, class edge.Class, hostname string) string {
	return resourceName(slug, class, hostname, "cert")
}

func negName(slug string, class edge.Class, hostname string) string {
	return resourceName(slug, class, hostname, "neg")
}

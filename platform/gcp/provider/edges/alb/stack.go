package alb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

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

func (s *stack) Promote(ctx context.Context, promotion edge.Promotion, pointer string, progress edge.Progress) error {
	if err := pin.Promote(ctx, s.ledger(), s.e.deps.Pins, promotion, pointer, progress); err != nil {
		return err
	}
	return s.released(ctx, promotion, progress)
}

func (s *stack) released(ctx context.Context, promotion edge.Promotion, progress edge.Progress) error {
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
		if progress != nil {
			progress.Detail("Routing " + hostname + " to " + hosts[hostname].Service)
		}
		if err := s.reach(ctx, hosts[hostname], hostname); err != nil {
			return errors.Join(err, s.raise(ctx, s.held.Hosts))
		}
	}
	s.held.Hosts = hosts
	s.keep()
	return nil
}

func (s *stack) RemovePointer(ctx context.Context, pointer string, _ edge.Progress) (edge.PruneResult, error) {
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
	if base, wild := strings.CutPrefix(binding.Hostname, "*."); wild {
		return providerkit.Refuse(providerkit.CodeInvalid,
			"this project declares %s as its own preview domain, and the %q edge cannot serve one: it resolves a preview by handing the "+
				"hostname's first label to Cloud Run as a service name, and a project's own wildcard leaves the project out of that label, "+
				"so two projects previewing the same branch would name one service and one would answer for the other.\n"+
				"Remove domains.preview from this project and run `ocel domain use '%s' --preview` against the bootstrap instead, "+
				"which serves every project's previews from one wildcard with the project in the label",
			edge.PreviewWildcard(base), Kind, edge.PreviewWildcard(base))
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
	if err := s.reach(ctx, hosts[binding.Hostname], binding.Hostname); err != nil {
		return errors.Join(err, s.raise(ctx, s.held.Hosts), release())
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
		return s.e.deps.Stacks.Destroy(ctx, target, edge.DiscardProgress())
	}
	_, err := s.e.deps.Stacks.Up(ctx, target, bindingProgram(bindingSpec{
		Region:         s.e.deps.Region,
		Slug:           s.state.Slug,
		Class:          s.state.Class,
		CertificateMap: s.held.Front.CertificateMap,
		Hosts:          hosts,
	}), edge.DiscardProgress())
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

func (s *stack) claim(ctx context.Context, hostname string) (bool, error) {
	name := s.e.claim(s.state.Class, hostname)
	record, err := providerkit.Held(ctx, s.e.deps.Records, name)
	if err != nil {
		return false, fmt.Errorf("read what serves %s on the %s edge: %w", hostname, Kind, err)
	}
	owner := Surface(s.state.Slug, s.state.Class)
	if len(record.Bytes) > 0 {
		var held claim
		if err := json.Unmarshal(record.Bytes, &held); err != nil {
			return false, fmt.Errorf("decode what serves %s on the %s edge: %w", hostname, Kind, err)
		}
		if held.Owner == owner {
			return false, nil
		}
		return false, claimedBy(hostname, held.Owner)
	}
	encoded, err := json.Marshal(claim{Owner: owner})
	if err != nil {
		return false, fmt.Errorf("encode what serves %s on the %s edge: %w", hostname, Kind, err)
	}
	record.Bytes = encoded
	_, err = s.e.deps.Records.Write(ctx, record)
	if errors.Is(err, providerkit.ErrStale) {
		return false, s.claimedMeanwhile(ctx, hostname, name)
	}
	if err != nil {
		return false, fmt.Errorf("record what serves %s on the %s edge: %w", hostname, Kind, err)
	}
	return true, nil
}

func (s *stack) claimedMeanwhile(ctx context.Context, hostname string, name providerkit.RecordName) error {
	record, err := providerkit.Held(ctx, s.e.deps.Records, name)
	if err != nil || len(record.Bytes) == 0 {
		return providerkit.Refuse(providerkit.CodeBusy,
			"%s was claimed on the %s edge while this bind was claiming it: bind it again once the other run has finished", hostname, Kind)
	}
	var held claim
	if err := json.Unmarshal(record.Bytes, &held); err != nil {
		return fmt.Errorf("decode what serves %s on the %s edge: %w", hostname, Kind, err)
	}
	return claimedBy(hostname, held.Owner)
}

func claimedBy(hostname, owner string) error {
	return providerkit.Refuse(providerkit.CodeInvalid,
		"%s is served by %s on the %s edge, and one hostname is routed to one project: release it there with `ocel domain remove` first",
		hostname, owner, Kind)
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
		if err := s.e.deps.Stacks.Destroy(ctx, s.target(), edge.DiscardProgress()); err != nil {
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

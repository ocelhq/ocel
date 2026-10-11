package alb

import (
	"context"
	"maps"
	"slices"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/refusal"
)

type notices struct {
	progress.Log
	notify func(string)
}

func (n notices) Say(message string) { n.notify(message) }

func sayingTo(say func(string)) progress.Log {
	if say == nil {
		return progress.Discard()
	}
	return notices{Log: progress.Discard(), notify: say}
}

func (e *Edge) removeGate(ctx context.Context) error {
	_, attached, err := e.readGatePublication(ctx)
	if err != nil {
		return err
	}
	if attached {
		if err := e.deps.Stacks.Destroy(ctx, e.gateTarget(), progress.Discard()); err != nil {
			return err
		}
	}
	if err := e.forgetGates(ctx); err != nil {
		return err
	}
	return e.deps.Images.Delete(ctx)
}

func (s *stack) isGated() bool { return isGated(s.state.Tier, s.e.deps.Shielded) }

func (s *stack) listRoutedHosts() []string {
	var hosts []string
	for _, hostname := range slices.Sorted(maps.Keys(s.recorded.Hosts)) {
		if !isWildcard(hostname) {
			hosts = append(hosts, hostname)
		}
	}
	return hosts
}

func (s *stack) syncGate(ctx context.Context, spec edge.StackSpec) error {
	if !s.isGated() {
		return nil
	}
	gate := spec.PreviewGate
	if gate != nil {
		_, attached, err := s.e.readGatePublication(ctx)
		if err != nil {
			return err
		}
		if !attached {
			return refusal.Refuse(refusal.CodeNotReady,
				"%s has a preview gate recorded, and the %s load balancer of this project's preview tier runs no gate plugin yet: run `ocel bootstrap` once to attach it, then deploy again",
				spec.Slug, Kind)
		}
	}
	routed := s.listRoutedHosts()
	newlyGated := false
	changed, err := s.e.changeGateRecord(ctx, spec.Slug, func(recorded gateRecord, found bool) (gateRecord, bool) {
		if gate == nil {
			return gateRecord{}, false
		}
		newlyGated = !found
		return gateRecord{Hosts: slices.Sorted(maps.Keys(toSet(recorded.Hosts, routed))), Gate: *gate}, true
	})
	if err != nil || !changed {
		return err
	}
	log := sayingTo(spec.Warn)
	proof := gateProof{Address: s.recorded.LoadBalancer.Address}
	if newlyGated {
		proof.Hosts = routed
	}
	return s.e.publishGates(ctx, proof, log)
}

func (s *stack) admitGatedHosts(ctx context.Context, hosts []string, log progress.Log) error {
	if !s.isGated() || len(hosts) == 0 {
		return nil
	}
	if log == nil {
		log = progress.Discard()
	}
	_, gated, err := s.e.readGateRecord(ctx, s.state.Slug)
	if err != nil || !gated {
		return err
	}
	if _, err := s.e.changeGateRecord(ctx, s.state.Slug, func(recorded gateRecord, _ bool) (gateRecord, bool) {
		recorded.Hosts = slices.Sorted(maps.Keys(toSet(recorded.Hosts, hosts)))
		return recorded, true
	}); err != nil {
		return err
	}
	return s.e.publishGates(ctx, gateProof{Hosts: hosts, Address: s.recorded.LoadBalancer.Address}, log)
}

func (s *stack) forgetGatedHosts(ctx context.Context, hosts []string, log progress.Log) {
	if !s.isGated() || len(hosts) == 0 {
		return
	}
	_, err := s.e.changeGateRecord(ctx, s.state.Slug, func(recorded gateRecord, found bool) (gateRecord, bool) {
		if !found {
			return recorded, false
		}
		recorded.Hosts = slices.DeleteFunc(slices.Clone(recorded.Hosts), func(host string) bool { return slices.Contains(hosts, host) })
		return recorded, true
	})
	if err != nil {
		log.Warn("The preview gate still lists " + hosts[0] + ", which is no longer served: " + err.Error())
	}
}

func (s *stack) withdrawGate(ctx context.Context) error {
	if !s.isGated() {
		return nil
	}
	changed, err := s.e.changeGateRecord(ctx, s.state.Slug, func(gateRecord, bool) (gateRecord, bool) { return gateRecord{}, false })
	if err != nil || !changed {
		return err
	}
	return s.e.publishGates(ctx, gateProof{}, progress.Discard())
}

func toSet(lists ...[]string) map[string]struct{} {
	set := map[string]struct{}{}
	for _, list := range lists {
		for _, item := range list {
			set[item] = struct{}{}
		}
	}
	return set
}

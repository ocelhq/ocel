package alb

import (
	"context"
	"maps"
	"slices"
	"time"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/edge/gate"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
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

func (e *Edge) removeGate(ctx context.Context, tier environment.Tier) error {
	_, attached, err := e.readGatePublication(ctx, tier)
	if err != nil {
		return err
	}
	if attached {
		if err := e.deps.Stacks.Destroy(ctx, e.gateTarget(tier), progress.Discard()); err != nil {
			return err
		}
	}
	if err := e.forgetGates(ctx, tier); err != nil {
		return err
	}
	return e.deps.Images.Delete(ctx, tier)
}

func (s *stack) isGated() bool { return !s.e.deps.Shielded }

func (s *stack) listRoutedHosts() []string {
	var hosts []string
	for _, hostname := range slices.Sorted(maps.Keys(s.recorded.Hosts)) {
		if !isWildcard(hostname) {
			hosts = append(hosts, hostname)
		}
	}
	return hosts
}

func (s *stack) listRoutedHostRecords() map[string]Host {
	routed := map[string]Host{}
	for _, hostname := range s.listRoutedHosts() {
		routed[hostname] = s.recorded.Hosts[hostname]
	}
	return routed
}

func withRevalidateTokenDigests(digests map[string]string, admitted map[string]Host) map[string]string {
	kept := maps.Clone(digests)
	if kept == nil {
		kept = map[string]string{}
	}
	for hostname, host := range admitted {
		if host.RevalidateTokenDigest == "" {
			delete(kept, hostname)
			continue
		}
		kept[hostname] = host.RevalidateTokenDigest
	}
	if len(kept) == 0 {
		return nil
	}
	return kept
}

func (s *stack) syncGate(ctx context.Context, spec edge.StackSpec) error {
	if !s.isGated() {
		return nil
	}
	tier := s.state.Tier
	held := spec.Gate
	if held != nil {
		_, attached, err := s.e.readGatePublication(ctx, tier)
		if err != nil {
			return err
		}
		if !attached {
			return refusal.Refuse(refusal.CodeNotReady,
				"%s has a %s gate recorded, and the %s load balancer of this project's %s tier runs no gate plugin: run `ocel bootstrap` for the %s tier to attach it, then deploy again",
				spec.Slug, tier, Kind, tier, tier)
		}
	}
	routed := s.listRoutedHosts()
	newlyGated := false
	var record gateRecord
	changed, err := s.e.changeGateRecord(ctx, tier, spec.Slug, func(recorded gateRecord, found bool) (gateRecord, bool) {
		if held == nil {
			return gateRecord{}, false
		}
		newlyGated = !found
		record = gateRecord{
			Hosts:                  slices.Sorted(maps.Keys(toSet(recorded.Hosts, routed))),
			Gate:                   *held,
			RevalidateTokenDigests: withRevalidateTokenDigests(recorded.RevalidateTokenDigests, s.listRoutedHostRecords()),
		}
		return record, true
	})
	if err != nil {
		return err
	}
	if !changed {
		running, err := s.e.runsRecordedGate(ctx, tier, spec.Slug, record, held != nil)
		if err != nil || running {
			return err
		}
	}
	log := sayingTo(spec.Warn)
	proof := gateProof{Address: s.recorded.LoadBalancer.Address}
	if newlyGated {
		proof.Hosts = routed
	}
	return s.e.publishGates(ctx, tier, proof, log)
}

func (s *stack) admitGatedHosts(ctx context.Context, admitted map[string]Host, log progress.Log) error {
	if !s.isGated() || len(admitted) == 0 {
		return nil
	}
	hosts := slices.Sorted(maps.Keys(admitted))
	if log == nil {
		log = progress.Discard()
	}
	tier := s.state.Tier
	_, gated, err := s.e.readGateRecord(ctx, tier, s.state.Slug)
	if err != nil || !gated {
		return err
	}
	if _, err := s.e.changeGateRecord(ctx, tier, s.state.Slug, func(recorded gateRecord, _ bool) (gateRecord, bool) {
		recorded.Hosts = slices.Sorted(maps.Keys(toSet(recorded.Hosts, hosts)))
		recorded.RevalidateTokenDigests = withRevalidateTokenDigests(recorded.RevalidateTokenDigests, admitted)
		return recorded, true
	}); err != nil {
		return err
	}
	return s.e.publishGates(ctx, tier, gateProof{Hosts: hosts, Address: s.recorded.LoadBalancer.Address}, log)
}

func (s *stack) forgetGatedHosts(ctx context.Context, hosts []string, log progress.Log) {
	if !s.isGated() || len(hosts) == 0 {
		return
	}
	_, err := s.e.changeGateRecord(ctx, s.state.Tier, s.state.Slug, func(recorded gateRecord, found bool) (gateRecord, bool) {
		if !found {
			return recorded, false
		}
		recorded.Hosts = slices.DeleteFunc(slices.Clone(recorded.Hosts), func(host string) bool { return slices.Contains(hosts, host) })
		forgotten := map[string]Host{}
		for _, host := range hosts {
			forgotten[host] = Host{}
		}
		recorded.RevalidateTokenDigests = withRevalidateTokenDigests(recorded.RevalidateTokenDigests, forgotten)
		return recorded, true
	})
	if err != nil {
		log.Warn("The " + string(s.state.Tier) + " gate still lists " + hosts[0] + ", which is no longer served: " + err.Error())
	}
}

func (s *stack) withdrawGate(ctx context.Context) error {
	if !s.isGated() {
		return nil
	}
	tier := s.state.Tier
	changed, err := s.e.changeGateRecord(ctx, tier, s.state.Slug, func(gateRecord, bool) (gateRecord, bool) { return gateRecord{}, false })
	if err != nil {
		return err
	}
	if !changed {
		running, err := s.e.runsRecordedGate(ctx, tier, s.state.Slug, gateRecord{}, false)
		if err != nil || running {
			return err
		}
	}
	return s.e.publishGates(ctx, tier, gateProof{}, progress.Discard())
}

func (s *stack) signGateSession(ctx context.Context, hostname string) (string, error) {
	if !s.isGated() {
		inForce, err := stackrecords.ReadGate(ctx, s.e.deps.KeyValues, s.e.deps.Cipher, s.state.Tier, s.state.Slug)
		if err != nil || inForce == nil {
			return "", err
		}
		return gate.CookieName + "=" + inForce.SignSession(hostname, time.Now()), nil
	}
	recorded, gated, err := s.e.readGateRecord(ctx, s.state.Tier, s.state.Slug)
	if err != nil || !gated {
		return "", err
	}
	return gate.CookieName + "=" + recorded.Gate.SignSession(hostname, time.Now()), nil
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

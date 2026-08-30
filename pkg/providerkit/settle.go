package providerkit

import (
	"context"
	"fmt"
	"time"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type Resolver interface {
	Serving(ctx context.Context, hostname string) (edge.Kind, error)
}

const (
	settleAttempts = 12
	settleWait     = 5 * time.Second
)

type settler struct {
	kind     edge.Kind
	unbound  bool
	writer   edge.DNSWriter
	zone     string
	resolve  Resolver
	attempts int
	wait     time.Duration
	sleep    func(context.Context, time.Duration) error
	now      func() time.Time
	ask      func(headline string, records []edge.Record, notes ...string)
}

func newSettler(front edge.Edge, writer edge.DNSWriter, zone string, resolve Resolver) settler {
	return settler{
		kind:     front.Kind(),
		unbound:  front.Facts().ServesUnbound,
		writer:   writer,
		zone:     zone,
		resolve:  resolve,
		attempts: settleAttempts,
		wait:     settleWait,
		sleep:    sleep,
		now:      time.Now,
	}
}

type Prober interface {
	Serving(ctx context.Context, kind edge.Kind, hostname string) (edge.Kind, error)
}

type Diagnoser interface {
	Unreached(hostname string) string
}

func resolving(provider Provider, front edge.Edge, stand Resolver) Resolver {
	prober, ok := provider.(Prober)
	if !ok {
		return stand
	}
	return probing{prober: prober, kind: front.Kind()}
}

type probing struct {
	prober Prober
	kind   edge.Kind
}

func (p probing) Serving(ctx context.Context, hostname string) (edge.Kind, error) {
	return p.prober.Serving(ctx, p.kind, hostname)
}

func (p probing) Unreached(hostname string) string {
	diagnoser, held := p.prober.(Diagnoser)
	if !held {
		return ""
	}
	return diagnoser.Unreached(hostname)
}

func boundBy(front edge.Edge, state func() edge.StackState) Resolver {
	return boundResolver{kind: front.Kind(), unbound: front.Facts().ServesUnbound, state: state}
}

// TODO(#390): stands in for a provider that supplies no Prober, and answers from what
// the kit just bound rather than from what the hostname serves.
type boundResolver struct {
	kind    edge.Kind
	unbound bool
	state   func() edge.StackState
}

func (r boundResolver) Serving(_ context.Context, hostname string) (edge.Kind, error) {
	held := r.state()
	if edge.Pointable(edge.TargetOf(r.kind, r.unbound, held), held.Bound, hostname) {
		return r.kind, nil
	}
	return "", nil
}

func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (s settler) recordsFor(state edge.StackState, hostname string) ([]edge.Record, error) {
	target := edge.TargetOf(s.kind, s.unbound, state)
	if !edge.Pointable(target, state.Bound, hostname) {
		return nil, nil
	}
	return edge.RecordsFor(target, []string{hostname})
}

type recordSet struct {
	Written []edge.Record
	Owed    []edge.Record
}

func (s settler) write(ctx context.Context, records []edge.Record, headline string, say func(string), notes ...string) (recordSet, error) {
	var settled recordSet
	if len(records) == 0 {
		return settled, nil
	}
	for _, rec := range records {
		if note := rec.ApexNote(s.zone); note != "" {
			say(note)
		}
	}
	if s.writer == nil {
		settled.Owed = records
		if s.ask != nil {
			s.ask(headline, records, notes...)
		}
		return settled, nil
	}
	for _, rec := range records {
		say("Writing " + rec.String())
	}
	written, err := s.writer.EnsureRecords(ctx, records, say)
	settled.Written, settled.Owed = written, edge.Unwritten(records, written)
	return settled, err
}

func (s settler) release(ctx context.Context, written []edge.Record, say func(string)) error {
	if s.writer == nil || len(written) == 0 {
		return nil
	}
	for _, rec := range written {
		say("Removing " + rec.String())
	}
	return s.writer.DeleteRecords(ctx, written)
}

func (s settler) await(ctx context.Context, hostname string, say func(string)) (Probe, error) {
	attempts := max(s.attempts, 1)
	began := s.now()
	var serving edge.Kind
	for attempt := range attempts {
		var err error
		if serving, err = s.resolve.Serving(ctx, hostname); err != nil {
			return Probe{At: s.now().Unix(), Edge: serving}, err
		}
		if serving == s.kind {
			return Probe{At: s.now().Unix(), OK: true, Edge: serving}, nil
		}
		if attempt == attempts-1 {
			break
		}
		say(fmt.Sprintf("Waiting for %s to answer as the %s edge", hostname, s.kind))
		if err := s.sleep(ctx, s.wait); err != nil {
			return Probe{At: s.now().Unix(), Edge: serving}, err
		}
	}
	return Probe{At: s.now().Unix(), Edge: serving}, s.unresolved(hostname, serving, began)
}

func (s settler) unresolved(hostname string, serving edge.Kind, began time.Time) error {
	waited := s.now().Sub(began).Round(time.Second)
	if serving == "" {
		return Refuse(CodeNotReady,
			"%s does not answer as the %s edge yet%s — this run gave up after about %s, and re-running it picks up where this one stopped",
			hostname, s.kind, s.unreached(hostname), waited)
	}
	return Refuse(CodeNotReady,
		"%s answers as the %s edge, not the %s one this project deploys to — this run gave up after about %s",
		hostname, serving, s.kind, waited)
}

func (s settler) unreached(hostname string) string {
	diagnoser, held := s.resolve.(Diagnoser)
	if !held {
		return ""
	}
	cause := diagnoser.Unreached(hostname)
	if cause == "" {
		return ""
	}
	return ", and the last attempt to reach it ended in: " + cause
}

package providerkit

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	settleBudget   = time.Minute
	attendedBudget = 15 * time.Minute
	attemptWindow  = 20 * time.Second
	settleWait     = 5 * time.Second
)

type settlement struct {
	kind     edge.Kind
	unbound  bool
	dns      edge.DNSRecords
	zone     string
	liveness Liveness
	budget   time.Duration
	window   time.Duration
	wait     time.Duration
	sleep    func(context.Context, time.Duration) error
	now      func() time.Time
	owed     owedPolicy
}

type owedPolicy struct {
	ask        func(headline string, records []edge.Record, notes ...string)
	unattended bool
}

func attended(sender *eventStream) owedPolicy {
	return owedPolicy{ask: func(headline string, records []edge.Record, notes ...string) {
		sender.send(dnsOwedEvent(headline, records, notes...))
	}}
}

func (s *settlement) attend(sender *eventStream) {
	s.owed = attended(sender)
	s.budget = attendedBudget
}

func unattended(sender *eventStream) owedPolicy {
	policy := attended(sender)
	policy.unattended = true
	return policy
}

func newSettlement(front edge.Edge, dns edge.DNSRecords, zone string, liveness Liveness) settlement {
	return settlement{
		kind:     front.Kind(),
		unbound:  front.Facts().ServesUnbound,
		dns:      dns,
		zone:     zone,
		liveness: liveness,
		budget:   settleBudget,
		window:   attemptWindow,
		wait:     settleWait,
		sleep:    sleep,
		now:      time.Now,
	}
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

func (s settlement) recordsFor(state edge.StackState, hostname string) ([]edge.Record, error) {
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

const instructionsOnly = "This project has no DNS writer configured, so ocel wrote none of them and changed nothing at your DNS provider."

func (s settlement) write(ctx context.Context, records []edge.Record, headline string, say func(string), notes ...string) (recordSet, error) {
	var settled recordSet
	if len(records) == 0 {
		return settled, nil
	}
	for _, rec := range records {
		if note := rec.ApexNote(s.zone); note != "" {
			say(note)
		}
	}
	if s.dns == nil {
		settled.Owed = records
		if s.owed.ask != nil {
			s.owed.ask(headline, records, append(slices.Clone(notes), instructionsOnly)...)
		}
		return settled, s.waiting(headline, settled.Owed)
	}
	for _, rec := range records {
		say("Writing " + rec.String())
	}
	written, err := s.dns.Ensure(ctx, records, say)
	settled.Written, settled.Owed = written, edge.Unwritten(records, written)
	if err != nil || len(settled.Owed) == 0 {
		return settled, err
	}
	if s.owed.unattended && s.owed.ask != nil {
		s.owed.ask(headline, settled.Owed, notes...)
	}
	return settled, s.waiting(headline, settled.Owed)
}

type owedRecords struct {
	headline string
	records  []edge.Record
}

func (o owedRecords) Error() string {
	return fmt.Sprintf("%s — ocel did not write %s; once that is in place, `ocel domain add` waits for it and settles the rest",
		o.headline, strings.Join(recordLines(o.records), ", "))
}

func (s settlement) waiting(headline string, owed []edge.Record) error {
	if !s.owed.unattended || len(owed) == 0 {
		return nil
	}
	return Pending(owedRecords{headline: headline, records: owed})
}

func (s settlement) release(ctx context.Context, written []edge.Record, say func(string)) error {
	if s.dns == nil || len(written) == 0 {
		return nil
	}
	for _, rec := range written {
		say("Removing " + rec.String())
	}
	return s.dns.Delete(ctx, written)
}

func (s settlement) await(ctx context.Context, hostname string, say func(string)) (Probe, error) {
	began := s.now()
	deadline := began.Add(s.budget)
	bounded, stop := context.WithTimeout(ctx, s.budget)
	defer stop()
	var serving edge.Kind
	var outlasted string
	for {
		var err error
		serving, err = s.attempt(bounded, hostname)
		switch {
		case err == nil:
			outlasted = ""
		case ctx.Err() != nil:
			return Probe{At: s.now().Unix(), Edge: serving}, ctx.Err()
		case bounded.Err() != nil:
			return Probe{At: s.now().Unix()}, s.unresolved(hostname, "", began, outlasted)
		case errors.Is(err, context.DeadlineExceeded):
			serving, outlasted = "", fmt.Sprintf("the last attempt got no answer within %s", s.window)
		default:
			return Probe{At: s.now().Unix(), Edge: serving}, err
		}
		if serving == s.kind {
			return Probe{At: s.now().Unix(), OK: true, Edge: serving}, nil
		}
		if !s.now().Add(s.wait).Before(deadline) {
			break
		}
		say(fmt.Sprintf("Waiting for %s to answer as the %s edge", hostname, s.kind))
		if err := s.sleep(bounded, s.wait); err != nil {
			if ctx.Err() != nil {
				return Probe{At: s.now().Unix(), Edge: serving}, ctx.Err()
			}
			break
		}
	}
	return Probe{At: s.now().Unix(), Edge: serving}, s.unresolved(hostname, serving, began, outlasted)
}

func (s settlement) attempt(ctx context.Context, hostname string) (edge.Kind, error) {
	asking, stop := context.WithTimeout(ctx, s.window)
	defer stop()
	return s.liveness.ServingEdge(asking, s.kind, hostname)
}

func (s settlement) unresolved(hostname string, serving edge.Kind, began time.Time, outlasted string) error {
	waited := s.now().Sub(began).Round(time.Second)
	if serving == "" {
		cause := s.unreached(hostname)
		if outlasted != "" {
			cause += ", and " + outlasted
		}
		return Pending(refusal.Refuse(refusal.CodeNotReady,
			"%s does not answer as the %s edge yet%s — this run gave up after about %s, and `ocel domain add` picks up where it stopped",
			hostname, s.kind, cause, waited))
	}
	return Pending(refusal.Refuse(refusal.CodeNotReady,
		"%s answers as the %s edge, not the %s one this project deploys to — this run gave up after about %s",
		hostname, serving, s.kind, waited))
}

func (s settlement) unreached(hostname string) string {
	cause := s.liveness.Unreached(hostname)
	if cause == "" {
		return ""
	}
	return ", and the last attempt to reach it ended in: " + cause
}

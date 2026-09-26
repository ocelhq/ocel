package providerserver

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	"github.com/ocelhq/ocel/pkg/providerkit/stackrecords"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	cutoverBudget      = time.Minute
	manualRecordBudget = 15 * time.Minute
	attemptWindow      = 20 * time.Second
	cutoverWait        = 5 * time.Second
)

type dnsCutover struct {
	kind     edge.Kind
	unbound  bool
	dns      edge.DNSRecords
	zone     string
	liveness provider.Liveness
	budget   time.Duration
	window   time.Duration
	wait     time.Duration
	sleep    func(context.Context, time.Duration) error
	now      func() time.Time
	manual   manualRecordPolicy
}

type manualRecordPolicy struct {
	report func(headline string, records []edge.Record, notes ...string)
	fail   bool
}

func reportManualRecords(sender *eventStream) manualRecordPolicy {
	return manualRecordPolicy{report: func(headline string, records []edge.Record, notes ...string) {
		sender.send(dnsManualRecordsEvent(headline, records, notes...))
	}}
}

func (s *dnsCutover) waitForManualRecords(sender *eventStream) {
	s.manual = reportManualRecords(sender)
	s.budget = manualRecordBudget
}

func failOnManualRecords(sender *eventStream) manualRecordPolicy {
	policy := reportManualRecords(sender)
	policy.fail = true
	return policy
}

func newDNSCutover(front edge.Edge, dns edge.DNSRecords, zone string, liveness provider.Liveness) dnsCutover {
	return dnsCutover{
		kind:     front.Kind(),
		unbound:  front.Facts().ServesUnbound,
		dns:      dns,
		zone:     zone,
		liveness: liveness,
		budget:   cutoverBudget,
		window:   attemptWindow,
		wait:     cutoverWait,
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

func (s dnsCutover) recordsFor(state edge.StackState, hostname string) ([]edge.Record, error) {
	target := edge.TargetOf(s.kind, s.unbound, state)
	if !edge.Pointable(target, state.Bound, hostname) {
		return nil, nil
	}
	return edge.RecordsFor(target, []string{hostname})
}

type recordSet struct {
	Written []edge.Record
	Manual  []edge.Record
}

const instructionsOnly = "This project has no DNS writer configured, so ocel wrote none of them and changed nothing at your DNS provider."

func (s dnsCutover) write(ctx context.Context, records []edge.Record, headline string, say func(string), notes ...string) (recordSet, error) {
	var result recordSet
	if len(records) == 0 {
		return result, nil
	}
	for _, rec := range records {
		if note := rec.ApexNote(s.zone); note != "" {
			say(note)
		}
	}
	if s.dns == nil {
		result.Manual = records
		if s.manual.report != nil {
			s.manual.report(headline, records, append(slices.Clone(notes), instructionsOnly)...)
		}
		return result, s.waiting(headline, result.Manual)
	}
	for _, rec := range records {
		say("Writing " + rec.String())
	}
	written, err := s.dns.Ensure(ctx, records, say)
	result.Written, result.Manual = written, edge.Unwritten(records, written)
	if err != nil || len(result.Manual) == 0 {
		return result, err
	}
	if s.manual.fail && s.manual.report != nil {
		s.manual.report(headline, result.Manual, notes...)
	}
	return result, s.waiting(headline, result.Manual)
}

type manualRecordsPending struct {
	headline string
	records  []edge.Record
}

func (m manualRecordsPending) Error() string {
	return fmt.Sprintf("%s — ocel did not write %s; once that is in place, `ocel domain add` waits for it and finishes attaching the hostname",
		m.headline, strings.Join(recordLines(m.records), ", "))
}

func (s dnsCutover) waiting(headline string, manual []edge.Record) error {
	if !s.manual.fail || len(manual) == 0 {
		return nil
	}
	return provider.Resumable(manualRecordsPending{headline: headline, records: manual})
}

func (s dnsCutover) release(ctx context.Context, written []edge.Record, say func(string)) error {
	if s.dns == nil || len(written) == 0 {
		return nil
	}
	for _, rec := range written {
		say("Removing " + rec.String())
	}
	return s.dns.Delete(ctx, written)
}

func (s dnsCutover) await(ctx context.Context, hostname string, say func(string)) (stackrecords.ServeProbe, error) {
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
			return stackrecords.ServeProbe{At: s.now().Unix(), Edge: serving}, ctx.Err()
		case bounded.Err() != nil:
			return stackrecords.ServeProbe{At: s.now().Unix()}, s.unresolved(hostname, "", began, outlasted)
		case errors.Is(err, context.DeadlineExceeded):
			serving, outlasted = "", fmt.Sprintf("it got no answer within %s", s.window)
		default:
			return stackrecords.ServeProbe{At: s.now().Unix(), Edge: serving}, err
		}
		if serving == s.kind {
			return stackrecords.ServeProbe{At: s.now().Unix(), OK: true, Edge: serving}, nil
		}
		if !s.now().Add(s.wait).Before(deadline) {
			break
		}
		say(fmt.Sprintf("Waiting for %s to answer as the %s edge", hostname, s.kind))
		if err := s.sleep(bounded, s.wait); err != nil {
			if ctx.Err() != nil {
				return stackrecords.ServeProbe{At: s.now().Unix(), Edge: serving}, ctx.Err()
			}
			break
		}
	}
	return stackrecords.ServeProbe{At: s.now().Unix(), Edge: serving}, s.unresolved(hostname, serving, began, outlasted)
}

func (s dnsCutover) attempt(ctx context.Context, hostname string) (edge.Kind, error) {
	asking, stop := context.WithTimeout(ctx, s.window)
	defer stop()
	return s.liveness.ServingEdge(asking, s.kind, hostname)
}

func (s dnsCutover) unresolved(hostname string, serving edge.Kind, began time.Time, outlasted string) error {
	waited := s.now().Sub(began).Round(time.Second)
	if serving == "" {
		failure := s.lastProbeFailure(hostname)
		if outlasted != "" {
			failure += ", and " + outlasted
		}
		return provider.Resumable(refusal.Refuse(refusal.CodeNotReady,
			"%s does not answer as the %s edge yet%s — this run gave up after about %s, and `ocel domain add` picks up where it stopped",
			hostname, s.kind, failure, waited))
	}
	return provider.Resumable(refusal.Refuse(refusal.CodeNotReady,
		"%s answers as the %s edge, not the %s one this project deploys to — this run gave up after about %s",
		hostname, serving, s.kind, waited))
}

func (s dnsCutover) lastProbeFailure(hostname string) string {
	cause := s.liveness.LastProbeFailure(hostname)
	if cause == "" {
		return ""
	}
	return ", and the last attempt to reach it ended in: " + cause
}

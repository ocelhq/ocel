package domains

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/platform/aws/provider/certs"
	"github.com/ocelhq/ocel/platform/aws/provider/dns"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type Store interface {
	Save(ctx context.Context, settled Settlement) error
}

type Ask func(headline string, records []edge.Record, notes ...string)

type Engine struct {
	Kind          edge.Kind
	ServesUnbound bool
	Issuer        certs.Issuer
	Discarder     func(certs.Certificate) certs.Issuer
	Writer        edge.DNSWriter
	Poller        dns.Poller
	Prober        certs.Prober
	Store         Store
	Zone          string
	Open          func(edge.Kind) (edge.EdgeStack, error)
	Unbind        func(ctx context.Context, hostname string) error
	ProveNotes    []string
	Ask           Ask
}

type Target struct {
	Hostname string
	Pinned   string
	Surface  func(ctx context.Context, certificate string, say func(string)) (edge.DNSTarget, error)
}

func (e Engine) Settle(ctx context.Context, prior Settlement, cover []string, choose func(Settlement) []Target, say func(string)) (Settlement, error) {
	superseded := prior.Certificate
	supersededWritten := prior.Validation.Written

	state, err := e.certify(ctx, prior, cover, say)
	if err != nil {
		return state, err
	}
	for _, target := range choose(state) {
		state, err = e.settleHost(ctx, state, target, say)
		if err != nil {
			return state, err
		}
	}
	return state, e.discardSuperseded(ctx, state, superseded, supersededWritten, say)
}

func (e Engine) certify(ctx context.Context, prior Settlement, cover []string, say func(string)) (Settlement, error) {
	state := prior
	if e.Issuer.API == nil || len(cover) == 0 {
		return state, nil
	}
	checkpoint := func() error { return e.Store.Save(ctx, state) }

	if state.Certificate.Region != e.Issuer.Region || supersededBy(state.Certificate, cover) {
		state = state.WithoutCertificate()
	}
	if state.Certificate.Issued() {
		live, err := e.Issuer.Describe(ctx, state.Certificate)
		if err != nil && !certs.Gone(err) {
			return state, err
		}
		if err == nil && live.Issued() && live.CoversAll(cover) {
			state.Certificate = live
			return state, nil
		}
		say(fmt.Sprintf("Certificate %s no longer answers for %s in ACM; settling one that does", state.Certificate.ARN, strings.Join(cover, ", ")))
		state = state.WithoutCertificate()
	}

	if state.Certificate.ARN == "" {
		found, err := e.Issuer.Existing(ctx, cover)
		if err != nil {
			return state, err
		}
		if found.ARN != "" {
			say(fmt.Sprintf("Reusing certificate %s in %s: it already covers %s", found.ARN, e.Issuer.Region, strings.Join(cover, ", ")))
			state.Certificate = found
			return state, checkpoint()
		}
		say(fmt.Sprintf("Requesting a certificate for %s in %s", strings.Join(cover, ", "), e.Issuer.Region))
		cert, err := e.Issuer.Request(ctx, cover)
		if err != nil {
			return state, err
		}
		state.Certificate = cert
		if err := checkpoint(); err != nil {
			return state, err
		}
	}

	if len(state.Certificate.Validation) == 0 {
		cert, err := e.Issuer.AwaitValidation(ctx, state.Certificate, say)
		state.Certificate = cert
		if err != nil {
			return state, err
		}
	}

	validation, settleErr := e.settleRecords(ctx, state.Certificate.Validation, e.prove(cover), false, say, func(settled Records) error {
		state.Validation = settled
		return checkpoint()
	})
	state.Validation = validation
	if settleErr != nil {
		return state, settleErr
	}

	cert, err := e.Issuer.AwaitIssued(ctx, state.Certificate, say)
	state.Certificate = cert
	if recordErr := checkpoint(); recordErr != nil {
		return state, recordErr
	}
	return state, err
}

func supersededBy(cert certs.Certificate, cover []string) bool {
	return len(cert.Domains) > 0 && !cert.CoversAll(cover)
}

func (e Engine) settleHost(ctx context.Context, prior Settlement, target Target, say func(string)) (Settlement, error) {
	state := prior
	certificate, err := e.certificateFor(ctx, state, target)
	if err != nil {
		return state, err
	}
	host := state.Host(target.Hostname)
	serving := host.Serving()
	host.Certificate = certificate
	state = state.WithHost(host)
	if err := e.Store.Save(ctx, state); err != nil {
		return state, err
	}

	front, err := target.Surface(ctx, certificate, say)
	if err != nil {
		return state, err
	}
	if err := e.Store.Save(ctx, state); err != nil {
		return state, err
	}

	records, err := edge.RecordsFor(front, []string{target.Hostname})
	if err != nil {
		return state, err
	}
	for _, rec := range records {
		if note := rec.ApexNote(e.zoneOf(ctx, rec.Name)); note != "" {
			say(note)
		}
	}
	settled, err := e.settleRecords(ctx, records, e.point(target.Hostname), true, say, func(settled Records) error {
		host.Records = settled
		state = state.WithHost(host)
		return e.Store.Save(ctx, state)
	})
	host.Records = settled
	state = state.WithHost(host)
	if err != nil {
		return state, err
	}

	if !(host.Probe.OK && host.Probe.Edge == e.Kind) {
		probe, probeErr := e.Prober.Await(ctx, target.Hostname, e.Kind, append(slices.Clone(host.Records.Owed), state.Validation.Owed...), say)
		host.Probe = probe
		state = state.WithHost(host)
		if err := e.Store.Save(ctx, state); err != nil {
			return state, err
		}
		if probeErr != nil {
			return state, probeErr
		}
	}
	say(fmt.Sprintf("%s is served by the %s edge", target.Hostname, e.Kind))
	return state, e.retire(ctx, target.Hostname, serving, say)
}

func (e Engine) certificateFor(ctx context.Context, state Settlement, target Target) (string, error) {
	if target.Pinned != "" {
		if e.Issuer.API == nil {
			return "", nil
		}
		cert, err := e.Issuer.Pinned(ctx, target.Hostname, target.Pinned)
		if err != nil {
			return "", err
		}
		return cert.ARN, nil
	}
	return state.Certificate.ARN, nil
}

func (e Engine) settleRecords(ctx context.Context, records []edge.Record, ask func([]edge.Record), await bool, say func(string), checkpoint func(Records) error) (Records, error) {
	if len(records) == 0 {
		return Records{}, checkpoint(Records{})
	}
	var settled Records
	var writeErr error
	if e.Writer != nil {
		for _, rec := range records {
			say(fmt.Sprintf("Writing %s", rec))
		}
		settled.Written, writeErr = e.Writer.EnsureRecords(ctx, records, say)
		settled.Owed = edge.Unwritten(records, settled.Written)
	} else {
		settled.Owed = records
		if ask != nil {
			ask(records)
		}
	}
	if err := checkpoint(settled); err != nil {
		return settled, errors.Join(writeErr, err)
	}
	if writeErr != nil || e.Writer != nil || !await {
		return settled, writeErr
	}
	return settled, e.Poller.Await(ctx, records, say)
}

func (e Engine) prove(cover []string) func([]edge.Record) {
	return func(records []edge.Record) {
		if e.Ask == nil {
			return
		}
		e.Ask(
			fmt.Sprintf("Prove you own %s", strings.Join(bareNames(cover), ", ")),
			records,
			append([]string{"Leave it in place: ACM renews the certificate through it."}, e.ProveNotes...)...,
		)
	}
}

func bareNames(hostnames []string) []string {
	names := make([]string, 0, len(hostnames))
	for _, hostname := range hostnames {
		names = append(names, strings.TrimPrefix(hostname, "*."))
	}
	return names
}

func (e Engine) point(hostname string) func([]edge.Record) {
	return func(records []edge.Record) {
		if e.Ask == nil {
			return
		}
		e.Ask(fmt.Sprintf("Point %s at the %s edge", hostname, e.Kind), records)
	}
}

func (e Engine) retire(ctx context.Context, hostname string, serving edge.Kind, say func(string)) error {
	if serving == "" || serving == e.Kind || e.Open == nil {
		return nil
	}
	stack, err := e.Open(serving)
	if err != nil {
		return err
	}
	say(fmt.Sprintf("Unbinding %s from the %s edge it moved off", hostname, serving))
	if err := stack.UnbindDomain(ctx, hostname); err != nil {
		return err
	}
	say(fmt.Sprintf("%s answers on both edges until resolvers drop the record they hold: %s", hostname, e.flipWindow()))
	return nil
}

func (e Engine) flipWindow() string {
	if ttl := edge.WriteTTL(e.Writer); ttl > 0 {
		return ttl.String()
	}
	return "whatever TTL your DNS provider serves that record with"
}

func (e Engine) Withdraw(ctx context.Context, prior Settlement, hostnames []string, say func(string)) (Settlement, error) {
	state := prior
	for _, hostname := range hostnames {
		host := state.Host(hostname)
		say(fmt.Sprintf("Unbinding %s from the %s edge", hostname, e.Kind))
		if err := e.Unbind(ctx, hostname); err != nil {
			return state, err
		}
		if err := dns.Release(ctx, e.Writer, host.Records.Written, say); err != nil {
			return state, err
		}
		state = state.WithoutHost(hostname)
		if err := e.Store.Save(ctx, state); err != nil {
			return state, err
		}
	}
	return e.discardUnused(ctx, state, say)
}

func (e Engine) discardUnused(ctx context.Context, state Settlement, say func(string)) (Settlement, error) {
	if e.Discarder == nil || state.Certificate.ARN == "" || len(state.Hosts) > 0 {
		return state, nil
	}
	if err := e.discard(ctx, state.Certificate, state.Validation.Written, say); err != nil {
		return state, err
	}
	state = state.WithoutCertificate()
	return state, e.Store.Save(ctx, state)
}

func (e Engine) discardSuperseded(ctx context.Context, state Settlement, superseded certs.Certificate, written []edge.Record, say func(string)) error {
	if e.Discarder == nil || superseded.ARN == "" || superseded.ARN == state.Certificate.ARN || state.Uses(superseded.ARN) {
		return nil
	}
	if err := e.discard(ctx, superseded, edge.Unwritten(written, state.Validation.Written), say); err != nil {
		return err
	}
	return e.Store.Save(ctx, state)
}

func (e Engine) discard(ctx context.Context, cert certs.Certificate, written []edge.Record, say func(string)) error {
	if err := e.Discarder(cert).Discard(ctx, cert, say); err != nil {
		return err
	}
	return dns.Release(ctx, e.Writer, written, say)
}

func (e Engine) zoneOf(ctx context.Context, hostname string) string {
	finder, ok := e.Writer.(edge.ZoneFinder)
	if !ok {
		return e.Zone
	}
	zone, err := finder.ZoneOf(ctx, hostname)
	if err != nil {
		return e.Zone
	}
	return zone.Name
}

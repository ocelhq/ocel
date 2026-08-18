package certs

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type Settlement struct {
	Certificate Certificate
	Written     []edge.Record
	Owed        []edge.Record
	Probe       Probe
}

type Flow struct {
	Issuer Issuer
	Writer edge.DNSWriter
	Prober Prober
	Front  []edge.Record
}

func (f Flow) Settle(ctx context.Context, hostname string, kind edge.Kind, prior Settlement, say func(string), record func(Settlement) error) (Settlement, error) {
	state, err := f.Certificate(ctx, []string{hostname}, prior, say, record)
	if err != nil {
		return state, err
	}
	return f.Probe(ctx, hostname, kind, state, say, record)
}

func (f Flow) Certificate(ctx context.Context, hostnames []string, prior Settlement, say func(string), record func(Settlement) error) (Settlement, error) {
	state := prior
	checkpoint := func() error { return record(state) }

	if f.Issuer.API == nil {
		return state, nil
	}
	if state.Certificate.Region != f.Issuer.Region || supersededBy(state.Certificate, hostnames) {
		state.Certificate = Certificate{}
		state.Written, state.Owed = nil, nil
	}
	if state.Certificate.Issued() {
		live, err := f.Issuer.describe(ctx, state.Certificate)
		if err != nil && !gone(err) {
			return state, err
		}
		if err == nil && live.Issued() && live.CoversAll(hostnames) {
			state.Certificate = live
			return state, nil
		}
		say(fmt.Sprintf("Certificate %s no longer answers for %s in ACM; settling one that does", state.Certificate.ARN, strings.Join(hostnames, ", ")))
		state.Certificate = Certificate{}
		state.Written, state.Owed = nil, nil
	}

	if state.Certificate.ARN == "" {
		found, err := f.Issuer.Existing(ctx, hostnames)
		if err != nil {
			return state, err
		}
		if found.ARN != "" {
			say(fmt.Sprintf("Reusing certificate %s in %s: it already covers %s", found.ARN, f.Issuer.Region, strings.Join(hostnames, ", ")))
			state.Certificate = found
			return state, checkpoint()
		}
		say(fmt.Sprintf("Requesting a certificate for %s in %s", strings.Join(hostnames, ", "), f.Issuer.Region))
		cert, err := f.Issuer.Request(ctx, hostnames)
		if err != nil {
			return state, err
		}
		state.Certificate = cert
		if err := checkpoint(); err != nil {
			return state, err
		}
	}

	if len(state.Certificate.Validation) == 0 {
		cert, err := f.Issuer.AwaitValidation(ctx, state.Certificate)
		state.Certificate = cert
		if err != nil {
			return state, err
		}
	}

	written, owed, settleErr := f.settleValidation(ctx, state.Certificate.Validation, say)
	state.Written, state.Owed = written, owed
	if recordErr := checkpoint(); recordErr != nil {
		return state, errors.Join(settleErr, recordErr)
	}
	if settleErr != nil {
		return state, settleErr
	}

	cert, err := f.Issuer.AwaitIssued(ctx, state.Certificate, say)
	state.Certificate = cert
	if recordErr := checkpoint(); recordErr != nil {
		return state, recordErr
	}
	return state, err
}

func (f Flow) Probe(ctx context.Context, hostname string, kind edge.Kind, prior Settlement, say func(string), record func(Settlement) error) (Settlement, error) {
	state := prior
	if state.Probe.OK && state.Probe.Edge == kind {
		return state, nil
	}
	probe, err := f.Prober.Await(ctx, hostname, kind, append(slices.Clone(f.Front), state.Owed...), say)
	state.Probe = probe
	if recordErr := record(state); recordErr != nil {
		return state, recordErr
	}
	return state, err
}

func supersededBy(cert Certificate, hostnames []string) bool {
	return len(cert.Domains) > 0 && !cert.CoversAll(hostnames)
}

func (f Flow) settleValidation(ctx context.Context, records []edge.Record, say func(string)) ([]edge.Record, []edge.Record, error) {
	if len(records) == 0 {
		return nil, nil, nil
	}
	if f.Writer == nil {
		for _, rec := range records {
			say(fmt.Sprintf("Nothing writes DNS here — %s, and keep it there: ACM renews this certificate through it, so deleting it lets the certificate lapse", rec.Instruction()))
		}
		return nil, records, nil
	}
	for _, rec := range records {
		say(fmt.Sprintf("Writing %s", rec))
	}
	written, err := f.Writer.EnsureRecords(ctx, records, say)
	return written, edge.Unwritten(records, written), err
}

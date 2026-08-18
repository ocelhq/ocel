package certs

import (
	"context"
	"errors"
	"fmt"
	"slices"

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
	state := prior
	checkpoint := func() error { return record(state) }

	if f.Issuer.API != nil && state.Certificate.Region != f.Issuer.Region {
		state.Certificate = Certificate{}
		state.Written, state.Owed = nil, nil
	}

	if f.Issuer.API != nil && !state.Certificate.Issued() {
		if state.Certificate.ARN == "" {
			say(fmt.Sprintf("Requesting a certificate for %s in %s", hostname, f.Issuer.Region))
			cert, err := f.Issuer.Request(ctx, hostname)
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
		if err != nil {
			return state, err
		}
	}

	if state.Probe.OK && state.Probe.Edge == kind {
		return state, nil
	}
	probe, err := f.Prober.Await(ctx, hostname, kind, append(slices.Clone(f.Front), state.Owed...), say)
	state.Probe = probe
	if recordErr := checkpoint(); recordErr != nil {
		return state, recordErr
	}
	return state, err
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

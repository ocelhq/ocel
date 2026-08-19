package certs

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	acmtypes "github.com/aws/aws-sdk-go-v2/service/acm/types"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const wildcard = "*.preview.acme.com"

type fakeWriter struct {
	written []edge.Record
	err     error
}

func (f *fakeWriter) EnsureRecords(_ context.Context, records []edge.Record, _ func(string)) ([]edge.Record, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.written = append(f.written, records...)
	return records, nil
}

func (f *fakeWriter) DeleteRecords(context.Context, []edge.Record) error { return nil }

func passingProber(t *testing.T, kind string) Prober {
	t.Helper()
	return testProber(func(context.Context, string) (http.Header, error) {
		return headerNaming(kind), nil
	}, 3)
}

type recorder struct {
	steps []Settlement
}

func (r *recorder) record(s Settlement) error {
	r.steps = append(r.steps, s)
	return nil
}

func (r *recorder) last() Settlement {
	return r.steps[len(r.steps)-1]
}

func TestFlowSettle(t *testing.T) {
	t.Parallel()

	issuing := []acmStep{
		{status: StatusPendingValidation},
		{status: StatusPendingValidation, validation: true},
		{status: StatusIssued, validation: true},
	}

	t.Run("requests, validates, waits for issuance, then probes", func(t *testing.T) {
		t.Parallel()

		api := &fakeACM{steps: issuing}
		writer := &fakeWriter{}
		rec := &recorder{}
		flow := Flow{Issuer: testIssuer(api, 5), Writer: writer, Prober: passingProber(t, string(frontedEdgeKind))}

		got, err := flow.Settle(t.Context(), wildcard, frontedEdgeKind, Settlement{}, func(string) {}, rec.record)
		if err != nil {
			t.Fatalf("Settle: %v", err)
		}
		if !got.Certificate.Issued() || got.Certificate.ARN != testARN {
			t.Fatalf("certificate = %+v, want it issued", got.Certificate)
		}
		if len(writer.written) != 1 || writer.written[0] != validationRecord {
			t.Fatalf("written = %+v, want the validation record written", writer.written)
		}
		if len(got.Owed) != 0 {
			t.Errorf("owed = %+v, want nothing owed: ocel wrote the record", got.Owed)
		}
		if !got.Probe.OK || got.Probe.Edge != frontedEdgeKind {
			t.Errorf("probe = %+v, want it passing against the CloudFront edge", got.Probe)
		}
		if rec.steps[0].Certificate.ARN != testARN || rec.steps[0].Certificate.Issued() {
			t.Errorf("first checkpoint = %+v, want the ARN recorded before the wait", rec.steps[0])
		}
		if len(rec.steps) < 4 {
			t.Errorf("checkpoints = %d, want one per step so an interruption is resumable", len(rec.steps))
		}
	})

	t.Run("without a dns the validation record is owed, and the user is told to keep it", func(t *testing.T) {
		t.Parallel()

		api := &fakeACM{steps: issuing}
		var said []string
		var asked [][]edge.Record
		flow := Flow{Issuer: testIssuer(api, 5), Prober: passingProber(t, string(frontedEdgeKind)), Ask: func(records []edge.Record) {
			asked = append(asked, records)
		}}

		got, err := flow.Settle(t.Context(), wildcard, frontedEdgeKind, Settlement{}, func(m string) { said = append(said, m) }, func(Settlement) error { return nil })
		if err != nil {
			t.Fatalf("Settle: %v", err)
		}
		if len(got.Written) != 0 || len(got.Owed) != 1 || got.Owed[0] != validationRecord {
			t.Fatalf("written = %+v owed = %+v, want the record owed by the user", got.Written, got.Owed)
		}
		if len(asked) != 1 || len(asked[0]) != 1 || asked[0][0] != validationRecord {
			t.Errorf("asked = %+v, want the validation record handed over once for the user to add", asked)
		}
	})

	t.Run("resumes from a recorded ARN without requesting again", func(t *testing.T) {
		t.Parallel()

		api := &fakeACM{steps: []acmStep{{status: StatusPendingValidation, validation: true}, {status: StatusIssued, validation: true}}}
		flow := Flow{Issuer: testIssuer(api, 5), Writer: &fakeWriter{}, Prober: passingProber(t, string(frontedEdgeKind))}
		prior := Settlement{Certificate: Certificate{ARN: testARN, Region: CloudFrontRegion, Status: StatusPendingValidation}}

		got, err := flow.Settle(t.Context(), wildcard, frontedEdgeKind, prior, func(string) {}, func(Settlement) error { return nil })
		if err != nil {
			t.Fatalf("Settle: %v", err)
		}
		if len(api.requested) != 0 {
			t.Errorf("requested = %+v, want the recorded certificate reused", api.requested)
		}
		if !got.Certificate.Issued() {
			t.Errorf("certificate = %+v, want it carried to issued", got.Certificate)
		}
	})

	t.Run("resumes from an issued certificate straight into the probe", func(t *testing.T) {
		t.Parallel()

		api := &fakeACM{steps: []acmStep{{status: StatusIssued}}}
		writer := &fakeWriter{}
		flow := Flow{Issuer: testIssuer(api, 5), Writer: writer, Prober: passingProber(t, string(frontedEdgeKind))}
		prior := Settlement{Certificate: Certificate{ARN: testARN, Region: CloudFrontRegion, Status: StatusIssued, Domains: []string{wildcard}}}

		got, err := flow.Settle(t.Context(), wildcard, frontedEdgeKind, prior, func(string) {}, func(Settlement) error { return nil })
		if err != nil {
			t.Fatalf("Settle: %v", err)
		}
		if api.describes != 1 || len(writer.written) != 0 {
			t.Errorf("describes = %d written = %+v, want the recorded certificate re-read once and issuance skipped", api.describes, writer.written)
		}
		if !got.Probe.OK {
			t.Error("the probe did not run on a rerun of an already-issued certificate")
		}
	})

	t.Run("a recorded certificate ACM no longer has is settled again", func(t *testing.T) {
		t.Parallel()

		api := &fakeACM{steps: issuing, describeErr: &acmtypes.ResourceNotFoundException{}}
		flow := Flow{Issuer: testIssuer(api, 5), Writer: &fakeWriter{}, Prober: passingProber(t, string(frontedEdgeKind))}
		prior := Settlement{Certificate: Certificate{ARN: testARN, Region: CloudFrontRegion, Status: StatusIssued, Domains: []string{wildcard}}}

		got, err := flow.Settle(t.Context(), wildcard, frontedEdgeKind, prior, func(string) {}, func(Settlement) error { return nil })
		if err != nil {
			t.Fatalf("Settle: %v", err)
		}
		if len(api.requested) != 1 {
			t.Errorf("requested = %+v, want a certificate requested once the recorded one turned out to be gone", api.requested)
		}
		if !got.Certificate.Issued() || got.Certificate.ARN == "" {
			t.Errorf("certificate = %+v, want a live one", got.Certificate)
		}
	})

	t.Run("resumes from a passing probe without touching the network", func(t *testing.T) {
		t.Parallel()

		flow := Flow{Prober: testProber(func(context.Context, string) (http.Header, error) {
			t.Error("probed again after a probe already passed for this edge")
			return nil, nil
		}, 1)}
		prior := Settlement{Probe: Probe{OK: true, Edge: unboundEdgeKind}}

		if _, err := flow.Settle(t.Context(), wildcard, unboundEdgeKind, prior, func(string) {}, func(Settlement) error { return nil }); err != nil {
			t.Fatalf("Settle: %v", err)
		}
	})

	t.Run("a probe recorded against another edge is re-run", func(t *testing.T) {
		t.Parallel()

		probed := false
		flow := Flow{Prober: testProber(func(context.Context, string) (http.Header, error) {
			probed = true
			return headerNaming(string(unboundEdgeKind)), nil
		}, 2)}
		prior := Settlement{Probe: Probe{OK: true, Edge: frontedEdgeKind}}

		if _, err := flow.Settle(t.Context(), wildcard, unboundEdgeKind, prior, func(string) {}, func(Settlement) error { return nil }); err != nil {
			t.Fatalf("Settle: %v", err)
		}
		if !probed {
			t.Error("the project moved edges and the stale probe was taken at face value")
		}
	})

	t.Run("a rerun of a settled flow keeps the validation record recorded", func(t *testing.T) {
		t.Parallel()

		api := &fakeACM{steps: issuing}
		rec := &recorder{}
		flow := Flow{Issuer: testIssuer(api, 5), Writer: &fakeWriter{}, Prober: passingProber(t, string(frontedEdgeKind))}
		prior := Settlement{
			Certificate: Certificate{ARN: testARN, Region: CloudFrontRegion, Status: StatusIssued, Validation: []edge.Record{validationRecord}},
			Written:     []edge.Record{validationRecord},
			Probe:       Probe{OK: true, Edge: frontedEdgeKind},
		}

		got, err := flow.Settle(t.Context(), wildcard, frontedEdgeKind, prior, func(string) {}, rec.record)
		if err != nil {
			t.Fatalf("Settle: %v", err)
		}
		if len(got.Written) != 1 || got.Written[0] != validationRecord {
			t.Errorf("written = %+v, want the validation record still recorded: releasing the domain must delete it", got.Written)
		}
	})

	t.Run("a certificate recorded in another region is not reused", func(t *testing.T) {
		t.Parallel()

		api := &fakeACM{steps: issuing}
		flow := Flow{Issuer: testIssuer(api, 5), Writer: &fakeWriter{}, Prober: passingProber(t, string(frontedEdgeKind))}
		prior := Settlement{Certificate: Certificate{ARN: testARN, Region: "eu-west-2", Status: StatusIssued, Validation: []edge.Record{validationRecord}}}

		got, err := flow.Settle(t.Context(), wildcard, frontedEdgeKind, prior, func(string) {}, func(Settlement) error { return nil })
		if err != nil {
			t.Fatalf("Settle: %v", err)
		}
		if len(api.requested) != 1 {
			t.Fatalf("requested = %d, want a fresh certificate in %s: the recorded one is in another region", len(api.requested), CloudFrontRegion)
		}
		if got.Certificate.Region != CloudFrontRegion {
			t.Errorf("certificate = %+v, want it issued in %s", got.Certificate, CloudFrontRegion)
		}
	})

	t.Run("a probe timeout names the front record the user still owes", func(t *testing.T) {
		t.Parallel()

		front := edge.Record{Name: wildcard, Type: edge.RecordTypeCNAME, Value: "entry.ocel.dev"}
		flow := Flow{Front: []edge.Record{front}, Prober: testProber(func(context.Context, string) (http.Header, error) {
			return http.Header{}, nil
		}, 2)}

		_, err := flow.Settle(t.Context(), wildcard, unboundEdgeKind, Settlement{}, func(string) {}, func(Settlement) error { return nil })
		if err == nil || !strings.Contains(err.Error(), front.Value) {
			t.Fatalf("err = %v, want the outstanding front record named", err)
		}
	})

	t.Run("an edge that certifies nothing asks for no certificate but is still probed", func(t *testing.T) {
		t.Parallel()

		rec := &recorder{}
		flow := Flow{Prober: passingProber(t, string(unboundEdgeKind))}

		got, err := flow.Settle(t.Context(), wildcard, unboundEdgeKind, Settlement{}, func(string) {}, rec.record)
		if err != nil {
			t.Fatalf("Settle: %v", err)
		}
		if got.Certificate.ARN != "" {
			t.Errorf("certificate = %+v, want none: this edge terminates TLS itself", got.Certificate)
		}
		if !got.Probe.OK || got.Probe.Edge != unboundEdgeKind {
			t.Errorf("probe = %+v, want it passing against the edge header it answers with", got.Probe)
		}
		if !rec.last().Probe.OK {
			t.Error("the passing probe was never recorded")
		}
	})

	t.Run("an issuance timeout is recorded, refused, and names the outstanding record", func(t *testing.T) {
		t.Parallel()

		api := &fakeACM{steps: []acmStep{{status: StatusPendingValidation, validation: true}}}
		rec := &recorder{}
		flow := Flow{Issuer: testIssuer(api, 2), Prober: passingProber(t, string(frontedEdgeKind))}

		_, err := flow.Settle(t.Context(), wildcard, frontedEdgeKind, Settlement{}, func(string) {}, rec.record)
		if err == nil {
			t.Fatal("Settle err = nil, want the bounded wait to refuse")
		}
		if !strings.Contains(err.Error(), validationRecord.Name) {
			t.Errorf("err = %v, want the outstanding record named", err)
		}
		if rec.last().Certificate.ARN != testARN || len(rec.last().Owed) != 1 {
			t.Errorf("last checkpoint = %+v, want the certificate and the owed record recorded before the refusal", rec.last())
		}
	})

	t.Run("a probe timeout is recorded and refused", func(t *testing.T) {
		t.Parallel()

		rec := &recorder{}
		flow := Flow{Prober: testProber(func(context.Context, string) (http.Header, error) {
			return http.Header{}, nil
		}, 2)}

		_, err := flow.Settle(t.Context(), wildcard, unboundEdgeKind, Settlement{}, func(string) {}, rec.record)
		if err == nil {
			t.Fatal("Settle err = nil, want the probe to refuse")
		}
		if rec.last().Probe.OK {
			t.Error("a failed probe was recorded as passing")
		}
	})

	t.Run("a validation record ocel cannot write is surfaced and owed", func(t *testing.T) {
		t.Parallel()

		api := &fakeACM{steps: issuing}
		rec := &recorder{}
		flow := Flow{Issuer: testIssuer(api, 5), Writer: &fakeWriter{err: errors.New("zone is read-only")}, Prober: passingProber(t, string(frontedEdgeKind))}

		_, err := flow.Settle(t.Context(), wildcard, frontedEdgeKind, Settlement{}, func(string) {}, rec.record)
		if err == nil || !strings.Contains(err.Error(), "zone is read-only") {
			t.Fatalf("err = %v, want the write failure surfaced", err)
		}
		if len(rec.last().Owed) != 1 {
			t.Errorf("last checkpoint = %+v, want the unwritten record owed", rec.last())
		}
	})
}

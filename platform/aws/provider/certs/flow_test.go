package certs

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

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
		flow := Flow{Issuer: testIssuer(api, 5), Writer: writer, Prober: passingProber(t, "native")}

		got, err := flow.Settle(t.Context(), wildcard, edge.KindNative, Settlement{}, func(string) {}, rec.record)
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
		if !got.Probe.OK || got.Probe.Edge != edge.KindNative {
			t.Errorf("probe = %+v, want it passing against the native edge", got.Probe)
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
		flow := Flow{Issuer: testIssuer(api, 5), Prober: passingProber(t, "native")}

		got, err := flow.Settle(t.Context(), wildcard, edge.KindNative, Settlement{}, func(m string) { said = append(said, m) }, func(Settlement) error { return nil })
		if err != nil {
			t.Fatalf("Settle: %v", err)
		}
		if len(got.Written) != 0 || len(got.Owed) != 1 || got.Owed[0] != validationRecord {
			t.Fatalf("written = %+v owed = %+v, want the record owed by the user", got.Written, got.Owed)
		}
		notice := strings.Join(said, "\n")
		if !strings.Contains(notice, "renews") || !strings.Contains(notice, validationRecord.Name) {
			t.Errorf("said = %v, want the keep-the-record notice naming ACM renewal", said)
		}
	})

	t.Run("resumes from a recorded ARN without requesting again", func(t *testing.T) {
		t.Parallel()

		api := &fakeACM{steps: []acmStep{{status: StatusPendingValidation, validation: true}, {status: StatusIssued, validation: true}}}
		flow := Flow{Issuer: testIssuer(api, 5), Writer: &fakeWriter{}, Prober: passingProber(t, "native")}
		prior := Settlement{Certificate: Certificate{ARN: testARN, Region: CloudFrontRegion, Status: StatusPendingValidation}}

		got, err := flow.Settle(t.Context(), wildcard, edge.KindNative, prior, func(string) {}, func(Settlement) error { return nil })
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

		api := &fakeACM{steps: issuing}
		writer := &fakeWriter{}
		flow := Flow{Issuer: testIssuer(api, 5), Writer: writer, Prober: passingProber(t, "native")}
		prior := Settlement{Certificate: Certificate{ARN: testARN, Region: CloudFrontRegion, Status: StatusIssued}}

		got, err := flow.Settle(t.Context(), wildcard, edge.KindNative, prior, func(string) {}, func(Settlement) error { return nil })
		if err != nil {
			t.Fatalf("Settle: %v", err)
		}
		if api.describes != 0 || len(writer.written) != 0 {
			t.Errorf("describes = %d written = %+v, want issuance skipped entirely", api.describes, writer.written)
		}
		if !got.Probe.OK {
			t.Error("the probe did not run on a rerun of an already-issued certificate")
		}
	})

	t.Run("resumes from a passing probe without touching the network", func(t *testing.T) {
		t.Parallel()

		flow := Flow{Prober: testProber(func(context.Context, string) (http.Header, error) {
			t.Error("probed again after a probe already passed for this edge")
			return nil, nil
		}, 1)}
		prior := Settlement{Probe: Probe{OK: true, Edge: edge.KindCloudflare}}

		if _, err := flow.Settle(t.Context(), wildcard, edge.KindCloudflare, prior, func(string) {}, func(Settlement) error { return nil }); err != nil {
			t.Fatalf("Settle: %v", err)
		}
	})

	t.Run("a probe recorded against another edge is re-run", func(t *testing.T) {
		t.Parallel()

		probed := false
		flow := Flow{Prober: testProber(func(context.Context, string) (http.Header, error) {
			probed = true
			return headerNaming("cloudflare"), nil
		}, 2)}
		prior := Settlement{Probe: Probe{OK: true, Edge: edge.KindNative}}

		if _, err := flow.Settle(t.Context(), wildcard, edge.KindCloudflare, prior, func(string) {}, func(Settlement) error { return nil }); err != nil {
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
		flow := Flow{Issuer: testIssuer(api, 5), Writer: &fakeWriter{}, Prober: passingProber(t, "native")}
		prior := Settlement{
			Certificate: Certificate{ARN: testARN, Region: CloudFrontRegion, Status: StatusIssued, Validation: []edge.Record{validationRecord}},
			Written:     []edge.Record{validationRecord},
			Probe:       Probe{OK: true, Edge: edge.KindNative},
		}

		got, err := flow.Settle(t.Context(), wildcard, edge.KindNative, prior, func(string) {}, rec.record)
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
		flow := Flow{Issuer: testIssuer(api, 5), Writer: &fakeWriter{}, Prober: passingProber(t, "native")}
		prior := Settlement{Certificate: Certificate{ARN: testARN, Region: "eu-west-2", Status: StatusIssued, Validation: []edge.Record{validationRecord}}}

		got, err := flow.Settle(t.Context(), wildcard, edge.KindNative, prior, func(string) {}, func(Settlement) error { return nil })
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

		_, err := flow.Settle(t.Context(), wildcard, edge.KindCloudflare, Settlement{}, func(string) {}, func(Settlement) error { return nil })
		if err == nil || !strings.Contains(err.Error(), front.Value) {
			t.Fatalf("err = %v, want the outstanding front record named", err)
		}
	})

	t.Run("cloudflare asks for no certificate but is still probed", func(t *testing.T) {
		t.Parallel()

		rec := &recorder{}
		flow := Flow{Prober: passingProber(t, "cloudflare")}

		got, err := flow.Settle(t.Context(), wildcard, edge.KindCloudflare, Settlement{}, func(string) {}, rec.record)
		if err != nil {
			t.Fatalf("Settle: %v", err)
		}
		if got.Certificate.ARN != "" {
			t.Errorf("certificate = %+v, want none: cloudflare terminates TLS itself", got.Certificate)
		}
		if !got.Probe.OK || got.Probe.Edge != edge.KindCloudflare {
			t.Errorf("probe = %+v, want it passing against x-ocel-edge: cloudflare", got.Probe)
		}
		if !rec.last().Probe.OK {
			t.Error("the passing probe was never recorded")
		}
	})

	t.Run("an issuance timeout is recorded, refused, and names the outstanding record", func(t *testing.T) {
		t.Parallel()

		api := &fakeACM{steps: []acmStep{{status: StatusPendingValidation, validation: true}}}
		rec := &recorder{}
		flow := Flow{Issuer: testIssuer(api, 2), Prober: passingProber(t, "native")}

		_, err := flow.Settle(t.Context(), wildcard, edge.KindNative, Settlement{}, func(string) {}, rec.record)
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

		_, err := flow.Settle(t.Context(), wildcard, edge.KindCloudflare, Settlement{}, func(string) {}, rec.record)
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
		flow := Flow{Issuer: testIssuer(api, 5), Writer: &fakeWriter{err: errors.New("zone is read-only")}, Prober: passingProber(t, "native")}

		_, err := flow.Settle(t.Context(), wildcard, edge.KindNative, Settlement{}, func(string) {}, rec.record)
		if err == nil || !strings.Contains(err.Error(), "zone is read-only") {
			t.Fatalf("err = %v, want the write failure surfaced", err)
		}
		if len(rec.last().Owed) != 1 {
			t.Errorf("last checkpoint = %+v, want the unwritten record owed", rec.last())
		}
	})
}

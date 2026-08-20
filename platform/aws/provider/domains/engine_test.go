package domains

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/acm"
	acmtypes "github.com/aws/aws-sdk-go-v2/service/acm/types"

	"github.com/ocelhq/ocel/platform/aws/provider/certs"
	"github.com/ocelhq/ocel/platform/aws/provider/dns"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	wildcard                    = "*.preview.acme.com"
	testARN                     = "arn:aws:acm:us-east-1:111122223333:certificate/abcd-1234"
	unboundEdgeKind   edge.Kind = "unbound-edge"
	frontedEdgeKind   edge.Kind = "fronted-edge"
	frontedEntrypoint           = "entry.ocel.dev"
)

var validationRecord = edge.Record{
	Name:  "_ocel.preview.acme.com",
	Type:  edge.RecordTypeCNAME,
	Value: "_target.acm-validations.aws",
}

type acmStep struct {
	status     string
	validation bool
}

type fakeACM struct {
	steps       []acmStep
	requested   []*acm.RequestCertificateInput
	deleted     []string
	describes   int
	describeErr error
}

func (f *fakeACM) RequestCertificate(_ context.Context, in *acm.RequestCertificateInput, _ ...func(*acm.Options)) (*acm.RequestCertificateOutput, error) {
	f.requested = append(f.requested, in)
	arn := testARN
	if len(f.requested) > 1 {
		arn = fmt.Sprintf("%s-%d", testARN, len(f.requested))
	}
	return &acm.RequestCertificateOutput{CertificateArn: aws.String(arn)}, nil
}

func (f *fakeACM) DescribeCertificate(_ context.Context, in *acm.DescribeCertificateInput, _ ...func(*acm.Options)) (*acm.DescribeCertificateOutput, error) {
	f.describes++
	if f.describeErr != nil && f.describes == 1 {
		return nil, f.describeErr
	}
	step := f.steps[min(f.describes-1, len(f.steps)-1)]
	out := &acm.DescribeCertificateOutput{Certificate: &acmtypes.CertificateDetail{
		CertificateArn: in.CertificateArn,
		Status:         acmtypes.CertificateStatus(step.status),
		DomainName:     aws.String(wildcard),
	}}
	if step.validation {
		out.Certificate.DomainValidationOptions = []acmtypes.DomainValidation{{
			ResourceRecord: &acmtypes.ResourceRecord{
				Name:  aws.String(validationRecord.Name + "."),
				Type:  acmtypes.RecordTypeCname,
				Value: aws.String(validationRecord.Value + "."),
			},
		}}
	}
	return out, nil
}

func (f *fakeACM) DeleteCertificate(_ context.Context, in *acm.DeleteCertificateInput, _ ...func(*acm.Options)) (*acm.DeleteCertificateOutput, error) {
	f.deleted = append(f.deleted, aws.ToString(in.CertificateArn))
	return &acm.DeleteCertificateOutput{}, nil
}

func (f *fakeACM) ListCertificates(context.Context, *acm.ListCertificatesInput, ...func(*acm.Options)) (*acm.ListCertificatesOutput, error) {
	return &acm.ListCertificatesOutput{}, nil
}

func testIssuer(api certs.ACMAPI, attempts int) certs.Issuer {
	return certs.Issuer{
		API:      api,
		Region:   certs.CloudFrontRegion,
		Wait:     func(context.Context, time.Duration) error { return nil },
		Attempts: attempts,
		Every:    time.Millisecond,
	}
}

func testProber(get func(context.Context, string) (http.Header, error), attempts int) certs.Prober {
	return certs.Prober{
		Get:      get,
		Wait:     func(context.Context, time.Duration) error { return nil },
		Attempts: attempts,
		Every:    time.Millisecond,
		Now:      func() time.Time { return time.Unix(1755500000, 0).UTC() },
		Jitter:   func() float64 { return 0.5 },
	}
}

func headerNaming(kind string) http.Header {
	return http.Header{http.CanonicalHeaderKey(edge.HeaderEdge): []string{kind}}
}

func passingProber(kind edge.Kind) certs.Prober {
	return testProber(func(context.Context, string) (http.Header, error) {
		return headerNaming(string(kind)), nil
	}, 3)
}

type recorder struct {
	steps []Settlement
}

func (r *recorder) Save(_ context.Context, s Settlement) error {
	r.steps = append(r.steps, s)
	return nil
}

func (r *recorder) last() Settlement {
	return r.steps[len(r.steps)-1]
}

func surfaceFor(kind edge.Kind) Target {
	return Target{
		Hostname: wildcard,
		Surface: func(context.Context, string, func(string)) (edge.DNSTarget, error) {
			if kind == unboundEdgeKind {
				return edge.DNSTarget{Kind: kind, ServesUnbound: true}, nil
			}
			return edge.DNSTarget{Kind: kind, Front: frontedEntrypoint}, nil
		},
	}
}

func settleOnce(t *testing.T, engine Engine, prior Settlement, say func(string)) (Settlement, error) {
	t.Helper()
	if engine.Store == nil {
		engine.Store = &recorder{}
	}
	if engine.Poller.Lookup == nil {
		engine.Poller = dns.Poller{
			Lookup:   func(context.Context, string) ([]string, error) { return []string{"192.0.2.1"}, nil },
			Wait:     func(context.Context, time.Duration) error { return nil },
			Attempts: 1,
			Every:    time.Millisecond,
		}
	}
	engine.Kind = cmpOrDefault(engine.Kind, frontedEdgeKind)
	engine.ServesUnbound = engine.Kind == unboundEdgeKind
	return engine.Settle(t.Context(), prior, []string{wildcard}, func(Settlement) []Target {
		return []Target{surfaceFor(engine.Kind)}
	}, say)
}

func cmpOrDefault(kind, fallback edge.Kind) edge.Kind {
	if kind != "" {
		return kind
	}
	return fallback
}

func TestEngineSettle(t *testing.T) {
	t.Parallel()

	issuing := []acmStep{
		{status: certs.StatusPendingValidation},
		{status: certs.StatusPendingValidation, validation: true},
		{status: certs.StatusIssued, validation: true},
	}

	t.Run("requests, validates, waits for issuance, then binds and probes", func(t *testing.T) {
		t.Parallel()

		api := &fakeACM{steps: issuing}
		writer := &fakeWriter{}
		rec := &recorder{}
		got, err := settleOnce(t, Engine{Issuer: testIssuer(api, 5), Writer: writer, Prober: passingProber(frontedEdgeKind), Store: rec}, Settlement{}, func(string) {})
		if err != nil {
			t.Fatalf("Settle: %v", err)
		}
		if !got.Certificate.Issued() || got.Certificate.ARN != testARN {
			t.Fatalf("certificate = %+v, want it issued", got.Certificate)
		}
		if len(writer.written) < 1 || writer.written[0] != validationRecord {
			t.Fatalf("written = %+v, want the validation record written first", writer.written)
		}
		if len(got.Validation.Owed) != 0 {
			t.Errorf("owed = %+v, want nothing owed: ocel wrote the record", got.Validation.Owed)
		}
		host := got.Host(wildcard)
		if !host.Probe.OK || host.Probe.Edge != frontedEdgeKind {
			t.Errorf("probe = %+v, want it passing against the fronted edge", host.Probe)
		}
		if host.Certificate != testARN {
			t.Errorf("host certificate = %q, want the settled one bound", host.Certificate)
		}
		if rec.steps[0].Certificate.ARN != testARN || rec.steps[0].Certificate.Issued() {
			t.Errorf("first checkpoint = %+v, want the ARN recorded before the wait", rec.steps[0])
		}
		if len(rec.steps) < 4 {
			t.Errorf("checkpoints = %d, want one per step so an interruption is resumable", len(rec.steps))
		}
	})

	t.Run("the certificate settles before the surface is touched", func(t *testing.T) {
		t.Parallel()

		api := &fakeACM{steps: issuing}
		engine := Engine{Issuer: testIssuer(api, 5), Writer: &fakeWriter{}, Prober: passingProber(frontedEdgeKind), Store: &recorder{}, Kind: frontedEdgeKind}
		var surfacedWith string
		_, err := engine.Settle(t.Context(), Settlement{}, []string{wildcard}, func(settled Settlement) []Target {
			if !settled.Certificate.Issued() {
				t.Error("targets chosen before the certificate settled")
			}
			return []Target{{Hostname: wildcard, Surface: func(_ context.Context, certificate string, _ func(string)) (edge.DNSTarget, error) {
				surfacedWith = certificate
				return edge.DNSTarget{Kind: frontedEdgeKind, Front: frontedEntrypoint}, nil
			}}}
		}, func(string) {})
		if err != nil {
			t.Fatalf("Settle: %v", err)
		}
		if surfacedWith != testARN {
			t.Errorf("surfaced with %q, want the issued certificate handed to the binding", surfacedWith)
		}
	})

	t.Run("without a dns writer the validation record is owed, and the user is told to keep it", func(t *testing.T) {
		t.Parallel()

		api := &fakeACM{steps: issuing}
		var asked [][]edge.Record
		var headlines []string
		engine := Engine{
			Issuer: testIssuer(api, 5),
			Prober: passingProber(frontedEdgeKind),
			Ask: func(headline string, records []edge.Record, _ ...string) {
				headlines = append(headlines, headline)
				asked = append(asked, records)
			},
		}
		got, err := settleOnce(t, engine, Settlement{}, func(string) {})
		if err != nil {
			t.Fatalf("Settle: %v", err)
		}
		if len(got.Validation.Written) != 0 || len(got.Validation.Owed) != 1 || got.Validation.Owed[0] != validationRecord {
			t.Fatalf("written = %+v owed = %+v, want the record owed by the user", got.Validation.Written, got.Validation.Owed)
		}
		if len(asked) < 1 || len(asked[0]) != 1 || asked[0][0] != validationRecord {
			t.Errorf("asked = %+v, want the validation record handed over for the user to add", asked)
		}
		if len(headlines) < 1 || headlines[0] != "Prove you own preview.acme.com" {
			t.Errorf("headlines = %v, want ownership asked for the bare domain", headlines)
		}
	})

	t.Run("resumes from a recorded ARN without requesting again", func(t *testing.T) {
		t.Parallel()

		api := &fakeACM{steps: []acmStep{{status: certs.StatusPendingValidation, validation: true}, {status: certs.StatusIssued, validation: true}}}
		prior := Settlement{Certificate: certs.Certificate{ARN: testARN, Region: certs.CloudFrontRegion, Status: certs.StatusPendingValidation}}
		got, err := settleOnce(t, Engine{Issuer: testIssuer(api, 5), Writer: &fakeWriter{}, Prober: passingProber(frontedEdgeKind)}, prior, func(string) {})
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

	t.Run("resumes from an issued certificate straight into the surface and probe", func(t *testing.T) {
		t.Parallel()

		api := &fakeACM{steps: []acmStep{{status: certs.StatusIssued}}}
		writer := &fakeWriter{}
		prior := Settlement{Certificate: certs.Certificate{ARN: testARN, Region: certs.CloudFrontRegion, Status: certs.StatusIssued, Domains: []string{wildcard}}}
		got, err := settleOnce(t, Engine{Issuer: testIssuer(api, 5), Writer: writer, Prober: passingProber(frontedEdgeKind)}, prior, func(string) {})
		if err != nil {
			t.Fatalf("Settle: %v", err)
		}
		if api.describes != 1 {
			t.Errorf("describes = %d, want the recorded certificate re-read once and issuance skipped", api.describes)
		}
		if !got.Host(wildcard).Probe.OK {
			t.Error("the probe did not run on a rerun of an already-issued certificate")
		}
	})

	t.Run("a recorded certificate ACM no longer has is settled again", func(t *testing.T) {
		t.Parallel()

		api := &fakeACM{steps: issuing, describeErr: &acmtypes.ResourceNotFoundException{}}
		prior := Settlement{Certificate: certs.Certificate{ARN: testARN, Region: certs.CloudFrontRegion, Status: certs.StatusIssued, Domains: []string{wildcard}}}
		got, err := settleOnce(t, Engine{Issuer: testIssuer(api, 5), Writer: &fakeWriter{}, Prober: passingProber(frontedEdgeKind)}, prior, func(string) {})
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

		engine := Engine{
			Kind: unboundEdgeKind,
			Prober: testProber(func(context.Context, string) (http.Header, error) {
				t.Error("probed again after a probe already passed for this edge")
				return nil, nil
			}, 1),
		}
		prior := Settlement{Hosts: []Host{{Hostname: wildcard, Probe: certs.Probe{OK: true, Edge: unboundEdgeKind}}}}
		if _, err := settleOnce(t, engine, prior, func(string) {}); err != nil {
			t.Fatalf("Settle: %v", err)
		}
	})

	t.Run("a probe recorded against another edge is re-run", func(t *testing.T) {
		t.Parallel()

		probed := false
		engine := Engine{
			Kind: unboundEdgeKind,
			Prober: testProber(func(context.Context, string) (http.Header, error) {
				probed = true
				return headerNaming(string(unboundEdgeKind)), nil
			}, 2),
		}
		prior := Settlement{Hosts: []Host{{Hostname: wildcard, Probe: certs.Probe{OK: true, Edge: frontedEdgeKind}}}}
		if _, err := settleOnce(t, engine, prior, func(string) {}); err != nil {
			t.Fatalf("Settle: %v", err)
		}
		if !probed {
			t.Error("the project moved edges and the stale probe was taken at face value")
		}
	})

	t.Run("a rerun of a settled flow keeps the validation record recorded", func(t *testing.T) {
		t.Parallel()

		api := &fakeACM{steps: issuing}
		prior := Settlement{
			Certificate: certs.Certificate{ARN: testARN, Region: certs.CloudFrontRegion, Status: certs.StatusIssued, Validation: []edge.Record{validationRecord}},
			Validation:  Records{Written: []edge.Record{validationRecord}},
			Hosts:       []Host{{Hostname: wildcard, Probe: certs.Probe{OK: true, Edge: frontedEdgeKind}}},
		}
		got, err := settleOnce(t, Engine{Issuer: testIssuer(api, 5), Writer: &fakeWriter{}, Prober: passingProber(frontedEdgeKind)}, prior, func(string) {})
		if err != nil {
			t.Fatalf("Settle: %v", err)
		}
		if len(got.Validation.Written) != 1 || got.Validation.Written[0] != validationRecord {
			t.Errorf("written = %+v, want the validation record still recorded: releasing the domain must delete it", got.Validation.Written)
		}
	})

	t.Run("a certificate recorded in another region is not reused", func(t *testing.T) {
		t.Parallel()

		api := &fakeACM{steps: issuing}
		prior := Settlement{Certificate: certs.Certificate{ARN: testARN, Region: "eu-west-2", Status: certs.StatusIssued, Validation: []edge.Record{validationRecord}}}
		got, err := settleOnce(t, Engine{Issuer: testIssuer(api, 5), Writer: &fakeWriter{}, Prober: passingProber(frontedEdgeKind)}, prior, func(string) {})
		if err != nil {
			t.Fatalf("Settle: %v", err)
		}
		if len(api.requested) != 1 {
			t.Fatalf("requested = %d, want a fresh certificate in %s: the recorded one is in another region", len(api.requested), certs.CloudFrontRegion)
		}
		if got.Certificate.Region != certs.CloudFrontRegion {
			t.Errorf("certificate = %+v, want it issued in %s", got.Certificate, certs.CloudFrontRegion)
		}
	})

	t.Run("a probe timeout names the front record the user still owes", func(t *testing.T) {
		t.Parallel()

		engine := Engine{Prober: testProber(func(context.Context, string) (http.Header, error) {
			return http.Header{}, nil
		}, 2)}
		_, err := settleOnce(t, engine, Settlement{}, func(string) {})
		if err == nil || !strings.Contains(err.Error(), frontedEntrypoint) {
			t.Fatalf("err = %v, want the outstanding front record named", err)
		}
	})

	t.Run("an edge that certifies nothing asks for no certificate but is still probed", func(t *testing.T) {
		t.Parallel()

		rec := &recorder{}
		got, err := settleOnce(t, Engine{Kind: unboundEdgeKind, Prober: passingProber(unboundEdgeKind), Store: rec}, Settlement{}, func(string) {})
		if err != nil {
			t.Fatalf("Settle: %v", err)
		}
		if got.Certificate.ARN != "" {
			t.Errorf("certificate = %+v, want none: this edge terminates TLS itself", got.Certificate)
		}
		host := got.Host(wildcard)
		if !host.Probe.OK || host.Probe.Edge != unboundEdgeKind {
			t.Errorf("probe = %+v, want it passing against the edge header it answers with", host.Probe)
		}
		if !rec.last().Host(wildcard).Probe.OK {
			t.Error("the passing probe was never recorded")
		}
	})

	t.Run("an issuance timeout is recorded, refused, and names the outstanding record", func(t *testing.T) {
		t.Parallel()

		api := &fakeACM{steps: []acmStep{{status: certs.StatusPendingValidation, validation: true}}}
		rec := &recorder{}
		_, err := settleOnce(t, Engine{Issuer: testIssuer(api, 2), Prober: passingProber(frontedEdgeKind), Store: rec}, Settlement{}, func(string) {})
		if err == nil {
			t.Fatal("Settle err = nil, want the bounded wait to refuse")
		}
		if !strings.Contains(err.Error(), validationRecord.Name) {
			t.Errorf("err = %v, want the outstanding record named", err)
		}
		if rec.last().Certificate.ARN != testARN || len(rec.last().Validation.Owed) != 1 {
			t.Errorf("last checkpoint = %+v, want the certificate and the owed record recorded before the refusal", rec.last())
		}
	})

	t.Run("a probe timeout is recorded and refused", func(t *testing.T) {
		t.Parallel()

		rec := &recorder{}
		engine := Engine{Kind: unboundEdgeKind, Store: rec, Prober: testProber(func(context.Context, string) (http.Header, error) {
			return http.Header{}, nil
		}, 2)}
		_, err := settleOnce(t, engine, Settlement{}, func(string) {})
		if err == nil {
			t.Fatal("Settle err = nil, want the probe to refuse")
		}
		if rec.last().Host(wildcard).Probe.OK {
			t.Error("a failed probe was recorded as passing")
		}
	})

	t.Run("a validation record ocel cannot write is surfaced and owed", func(t *testing.T) {
		t.Parallel()

		api := &fakeACM{steps: issuing}
		rec := &recorder{}
		engine := Engine{Issuer: testIssuer(api, 5), Writer: &fakeWriter{err: errors.New("zone is read-only")}, Prober: passingProber(frontedEdgeKind), Store: rec}
		_, err := settleOnce(t, engine, Settlement{}, func(string) {})
		if err == nil || !strings.Contains(err.Error(), "zone is read-only") {
			t.Fatalf("err = %v, want the write failure surfaced", err)
		}
		if len(rec.last().Validation.Owed) != 1 {
			t.Errorf("last checkpoint = %+v, want the unwritten record owed", rec.last())
		}
	})
}

package server

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/acm"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	"github.com/ocelhq/ocel/platform/aws/provider/certs"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func TestRequirePreviewClass(t *testing.T) {
	t.Parallel()

	if err := requirePreviewClass(deploymentsv1.Environment_CLASS_PREVIEW); err != nil {
		t.Fatalf("requirePreviewClass: %v", err)
	}
	for _, class := range []deploymentsv1.Environment_Class{
		deploymentsv1.Environment_CLASS_PRODUCTION,
		deploymentsv1.Environment_CLASS_UNSPECIFIED,
	} {
		err := requirePreviewClass(class)
		if err == nil {
			t.Fatalf("expected %v to be refused", class)
		}
		if !strings.Contains(err.Error(), "--preview") {
			t.Errorf("error must name the flag, got %q", err)
		}
	}
}

func TestPreviewBaseDomainArg(t *testing.T) {
	t.Parallel()

	t.Run("normalizes the domain", func(t *testing.T) {
		t.Parallel()
		got, err := previewBaseDomainArg("  Preview.Acme.com. ")
		if err != nil {
			t.Fatalf("previewBaseDomainArg: %v", err)
		}
		if got != "preview.acme.com" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("refuses a wildcard and names the domain to pass", func(t *testing.T) {
		t.Parallel()
		_, err := previewBaseDomainArg("*.preview.acme.com")
		if err == nil {
			t.Fatal("expected a wildcard to be refused")
		}
		if !strings.Contains(err.Error(), "preview.acme.com") {
			t.Errorf("error must name the domain to pass instead, got %q", err)
		}
	})

	t.Run("refuses what is not a domain", func(t *testing.T) {
		t.Parallel()
		for _, in := range []string{"", "acme", "https://preview.acme.com"} {
			if _, err := previewBaseDomainArg(in); err == nil {
				t.Errorf("expected %q to be refused", in)
			}
		}
	})
}

func TestGlobalPreviewDomain(t *testing.T) {
	t.Parallel()

	recorded := bootstrap.PreviewDomain{BaseDomain: "preview.acme.com", CloudflareAccount: "acct", GrammarMin: 1, GrammarMax: 1}

	t.Run("is nil when nothing is recorded", func(t *testing.T) {
		t.Parallel()
		owner := func(context.Context, string) (string, error) { return "", nil }
		if got := globalPreviewDomain(context.Background(), owner, bootstrap.PreviewDomain{}); got != nil {
			t.Errorf("got %+v, want nil", got)
		}
	})

	t.Run("reports the route the shared entry actually holds", func(t *testing.T) {
		t.Parallel()
		var asked string
		owner := func(_ context.Context, hostname string) (string, error) {
			asked = hostname
			return edge.PreviewEntryOwner, nil
		}
		got := globalPreviewDomain(context.Background(), owner, recorded)
		if asked != "*.preview.acme.com" {
			t.Errorf("asked %q, want the wildcard hostname", asked)
		}
		if !got.GetRouteInstalled() {
			t.Error("RouteInstalled = false, want true")
		}
		if got.GetBaseDomain() != "preview.acme.com" || got.GetCloudflareAccount() != "acct" {
			t.Errorf("got %+v", got)
		}
	})

	t.Run("carries the certificate, the records and the last probe", func(t *testing.T) {
		t.Parallel()
		owner := func(context.Context, string) (string, error) { return edge.PreviewEntryOwner, nil }
		full := recorded
		full.Records = []edge.Record{{Name: "*.preview.acme.com", Type: edge.RecordTypeAAAA, Value: edge.ProxyPlaceholder, Proxied: true}}
		full.Owed = []edge.Record{{Name: "_ocel.preview.acme.com", Type: edge.RecordTypeCNAME, Value: "_target.acm-validations.aws"}}
		full.Certificate = certs.Certificate{ARN: "arn:aws:acm:us-east-1:111122223333:certificate/abcd-1234", Status: certs.StatusIssued}
		full.Probe = certs.Probe{At: time.Unix(1755500000, 0).UTC(), Edge: edge.KindCloudflare, OK: true}

		got := globalPreviewDomain(context.Background(), owner, full)
		if got.GetCertificateId() != full.Certificate.ARN || got.GetCertificateStatus() != certs.StatusIssued {
			t.Errorf("certificate = %q %q", got.GetCertificateId(), got.GetCertificateStatus())
		}
		if len(got.GetRecordsWritten()) != 1 || !strings.Contains(got.GetRecordsWritten()[0], "*.preview.acme.com") {
			t.Errorf("records written = %v", got.GetRecordsWritten())
		}
		if len(got.GetRecordsOwed()) != 1 || !strings.Contains(got.GetRecordsOwed()[0], "acm-validations") {
			t.Errorf("records owed = %v", got.GetRecordsOwed())
		}
		if !got.GetLastProbeOk() || got.GetLastProbeAt() != 1755500000 || got.GetLastProbeEdge() != "cloudflare" {
			t.Errorf("probe = %v %d %q", got.GetLastProbeOk(), got.GetLastProbeAt(), got.GetLastProbeEdge())
		}
		if globalPreviewDomain(context.Background(), owner, recorded).GetLastProbeAt() != 0 {
			t.Error("an unprobed domain carries a probe timestamp")
		}
	})

	t.Run("a foreign or missing owner is not installed", func(t *testing.T) {
		t.Parallel()
		for _, owner := range []routeOwnerFunc{
			func(context.Context, string) (string, error) { return "", nil },
			func(context.Context, string) (string, error) { return "someone-elses-worker", nil },
			func(context.Context, string) (string, error) { return "", errors.New("no zone") },
		} {
			if globalPreviewDomain(context.Background(), owner, recorded).GetRouteInstalled() {
				t.Error("RouteInstalled = true, want false")
			}
		}
	})
}

type stateSSM struct {
	params map[string]string
}

func (s *stateSSM) GetParameter(_ context.Context, in *ssm.GetParameterInput, _ ...func(*ssm.Options)) (*ssm.GetParameterOutput, error) {
	raw, ok := s.params[aws.ToString(in.Name)]
	if !ok {
		return nil, &ssmtypes.ParameterNotFound{}
	}
	return &ssm.GetParameterOutput{Parameter: &ssmtypes.Parameter{Value: aws.String(raw)}}, nil
}

func (s *stateSSM) PutParameter(_ context.Context, in *ssm.PutParameterInput, _ ...func(*ssm.Options)) (*ssm.PutParameterOutput, error) {
	s.params[aws.ToString(in.Name)] = aws.ToString(in.Value)
	return &ssm.PutParameterOutput{}, nil
}

func (s *stateSSM) GetParametersByPath(_ context.Context, in *ssm.GetParametersByPathInput, _ ...func(*ssm.Options)) (*ssm.GetParametersByPathOutput, error) {
	out := &ssm.GetParametersByPathOutput{}
	for name := range s.params {
		if strings.HasPrefix(name, aws.ToString(in.Path)) {
			out.Parameters = append(out.Parameters, ssmtypes.Parameter{Name: aws.String(name)})
		}
	}
	return out, nil
}

func (s *stateSSM) DeleteParameter(_ context.Context, in *ssm.DeleteParameterInput, _ ...func(*ssm.Options)) (*ssm.DeleteParameterOutput, error) {
	delete(s.params, aws.ToString(in.Name))
	return &ssm.DeleteParameterOutput{}, nil
}

func TestPriorSettlement(t *testing.T) {
	t.Parallel()

	validation := edge.Record{Name: "_ocel.preview.acme.com", Type: edge.RecordTypeCNAME, Value: "_target.acm-validations.aws"}
	front := edge.Record{Name: "*.preview.acme.com", Type: edge.RecordTypeAAAA, Value: edge.ProxyPlaceholder, Proxied: true}

	t.Run("carries the validation record ocel wrote into a rerun", func(t *testing.T) {
		t.Parallel()

		recorded := bootstrap.PreviewDomain{
			BaseDomain:  "preview.acme.com",
			Records:     []edge.Record{front, validation},
			Certificate: certs.Certificate{ARN: "arn", Region: "us-east-1", Status: certs.StatusIssued, Validation: []edge.Record{validation}},
		}
		prior := priorSettlement(recorded)
		if len(prior.Written) != 1 || prior.Written[0] != validation {
			t.Errorf("written = %+v, want only the validation record: the front records are settled on their own", prior.Written)
		}
		if len(prior.Owed) != 0 {
			t.Errorf("owed = %+v, want nothing owed", prior.Owed)
		}
	})

	t.Run("carries a validation record the user owns", func(t *testing.T) {
		t.Parallel()

		recorded := bootstrap.PreviewDomain{
			BaseDomain:  "preview.acme.com",
			Records:     []edge.Record{front},
			Owed:        []edge.Record{validation},
			Certificate: certs.Certificate{ARN: "arn", Region: "us-east-1", Status: certs.StatusIssued, Validation: []edge.Record{validation}},
		}
		prior := priorSettlement(recorded)
		if len(prior.Owed) != 1 || prior.Owed[0] != validation {
			t.Errorf("owed = %+v, want the record the user still owns", prior.Owed)
		}
		if len(prior.Written) != 0 {
			t.Errorf("written = %+v, want nothing claimed as written", prior.Written)
		}
	})

	t.Run("nothing issued carries no records", func(t *testing.T) {
		t.Parallel()

		prior := priorSettlement(bootstrap.PreviewDomain{BaseDomain: "preview.acme.com", Records: []edge.Record{front}})
		if len(prior.Written) != 0 || len(prior.Owed) != 0 {
			t.Errorf("written = %+v owed = %+v, want the front records left out", prior.Written, prior.Owed)
		}
	})
}

type releaseEdge struct {
	edge.Edge
	destroyed []string
}

func (e *releaseEdge) Kind() edge.Kind { return edge.KindCloudflare }

func (e *releaseEdge) DestroyPreviewWildcard(_ context.Context, baseDomain string) error {
	e.destroyed = append(e.destroyed, baseDomain)
	return nil
}

type releaseWriter struct {
	deleted []edge.Record
}

func (w *releaseWriter) EnsureRecords(_ context.Context, records []edge.Record, _ func(string)) ([]edge.Record, error) {
	return records, nil
}

func (w *releaseWriter) DeleteRecords(_ context.Context, records []edge.Record) error {
	w.deleted = append(w.deleted, records...)
	return nil
}

type releaseACM struct {
	deleted []string
}

func (a *releaseACM) RequestCertificate(context.Context, *acm.RequestCertificateInput, ...func(*acm.Options)) (*acm.RequestCertificateOutput, error) {
	return nil, errors.New("releasing a domain never requests a certificate")
}

func (a *releaseACM) DescribeCertificate(context.Context, *acm.DescribeCertificateInput, ...func(*acm.Options)) (*acm.DescribeCertificateOutput, error) {
	return nil, errors.New("releasing a domain never describes a certificate")
}

func (a *releaseACM) ListCertificates(context.Context, *acm.ListCertificatesInput, ...func(*acm.Options)) (*acm.ListCertificatesOutput, error) {
	return nil, errors.New("releasing a domain never lists certificates")
}

func (a *releaseACM) DeleteCertificate(_ context.Context, in *acm.DeleteCertificateInput, _ ...func(*acm.Options)) (*acm.DeleteCertificateOutput, error) {
	a.deleted = append(a.deleted, aws.ToString(in.CertificateArn))
	return &acm.DeleteCertificateOutput{}, nil
}

func TestReleaseDomain(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	validation := edge.Record{Name: "_ocel.preview.acme.com", Type: edge.RecordTypeCNAME, Value: "_target.acm-validations.aws"}
	front := edge.Record{Name: "*.preview.acme.com", Type: edge.RecordTypeAAAA, Value: edge.ProxyPlaceholder, Proxied: true}
	recorded := bootstrap.PreviewDomain{
		BaseDomain:  "preview.acme.com",
		Records:     []edge.Record{front, validation},
		Certificate: certs.Certificate{ARN: "arn:aws:acm:us-east-1:111122223333:certificate/abcd-1234", Region: "us-east-1", Status: certs.StatusIssued, Validation: []edge.Record{validation}},
	}

	ssmc := &stateSSM{params: map[string]string{}}
	if err := bootstrap.WritePreviewDomain(ctx, ssmc, bootstrap.ClassPreview, recorded); err != nil {
		t.Fatalf("WritePreviewDomain: %v", err)
	}
	front1 := &releaseEdge{}
	writer := &releaseWriter{}
	api := &releaseACM{}

	if err := releaseDomain(ctx, releaseDeps{
		ssm:    ssmc,
		edge:   front1,
		writer: writer,
		issuer: certs.Issuer{API: api, Region: recorded.Certificate.Region},
	}, recorded, func(string) {}); err != nil {
		t.Fatalf("releaseDomain: %v", err)
	}

	if len(front1.destroyed) != 1 || front1.destroyed[0] != recorded.BaseDomain {
		t.Errorf("destroyed = %v, want the shared entry on %s removed", front1.destroyed, recorded.BaseDomain)
	}
	if len(writer.deleted) != 2 {
		t.Errorf("deleted = %+v, want every record ocel wrote removed, the validation record included", writer.deleted)
	}
	if len(api.deleted) != 1 || api.deleted[0] != recorded.Certificate.ARN {
		t.Errorf("deleted certificates = %v, want %q discarded in the region it was issued in", api.deleted, recorded.Certificate.ARN)
	}
	got, err := bootstrap.ReadPreviewDomain(ctx, ssmc, bootstrap.ClassPreview)
	if err != nil {
		t.Fatalf("ReadPreviewDomain: %v", err)
	}
	if got.BaseDomain != "" {
		t.Errorf("recorded domain = %+v, want it deleted", got)
	}
}

func TestGlobalPreviewProjects(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	ssmc := &stateSSM{params: map[string]string{}}
	for slug, state := range map[string]edge.StackState{
		"ambient":     {edge.StackKeySlug: "ambient", edge.StackKeyGlobalPreview: "preview.acme.com"},
		"own-domain":  {edge.StackKeySlug: "own-domain"},
		"other-usage": {edge.StackKeySlug: "other-usage", edge.StackKeyGlobalPreview: "preview.old.com"},
	} {
		if err := bootstrap.WriteStackStateFor(ctx, ssmc, bootstrap.ClassPreview, slug, state); err != nil {
			t.Fatalf("WriteStackStateFor(%s): %v", slug, err)
		}
	}

	served, err := globalPreviewProjects(ctx, ssmc, []string{"ambient", "own-domain", "other-usage", "never-deployed"}, "preview.acme.com")
	if err != nil {
		t.Fatalf("globalPreviewProjects: %v", err)
	}
	if len(served) != 1 || served[0] != "ambient" {
		t.Errorf("served = %v, want [ambient]: a project on its own preview domain is not served by the substrate's", served)
	}
}

func TestGlobalPreviewAccountMismatch(t *testing.T) {
	recorded := bootstrap.PreviewDomain{BaseDomain: "preview.acme.com", CloudflareAccount: "recorded-acct"}

	t.Run("a match passes", func(t *testing.T) {
		t.Setenv(cloudflareAccountEnvVar, "recorded-acct")
		if err := globalPreviewAccountMismatch(recorded); err != nil {
			t.Fatalf("globalPreviewAccountMismatch: %v", err)
		}
	})

	t.Run("a mismatch refuses and names both accounts", func(t *testing.T) {
		t.Setenv(cloudflareAccountEnvVar, "ambient-acct")
		err := globalPreviewAccountMismatch(recorded)
		if err == nil {
			t.Fatal("expected a mismatched account to be refused")
		}
		for _, want := range []string{"recorded-acct", "ambient-acct", "preview.acme.com", "ocel domain release"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error must name %q, got %q", want, err)
			}
		}
	})

	t.Run("nothing recorded is nothing to compare", func(t *testing.T) {
		t.Setenv(cloudflareAccountEnvVar, "ambient-acct")
		if err := globalPreviewAccountMismatch(bootstrap.PreviewDomain{}); err != nil {
			t.Fatalf("globalPreviewAccountMismatch: %v", err)
		}
	})
}

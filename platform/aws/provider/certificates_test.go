package provider

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/acm"
	acmtypes "github.com/aws/aws-sdk-go-v2/service/acm/types"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/aws/provider/certs"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const issuedARN = "arn:aws:acm:us-east-1:111122223333:certificate/issued"

type silentReporter struct{}

func (silentReporter) Say(string)    {}
func (silentReporter) Detail(string) {}

func (silentReporter) Span(string, time.Time, time.Time, error, ...providerkit.Attr) {}

type stubACM struct {
	statuses  []string
	requested int
}

func (s *stubACM) RequestCertificate(context.Context, *acm.RequestCertificateInput, ...func(*acm.Options)) (*acm.RequestCertificateOutput, error) {
	s.requested++
	return &acm.RequestCertificateOutput{CertificateArn: aws.String(issuedARN)}, nil
}

func (s *stubACM) DescribeCertificate(_ context.Context, in *acm.DescribeCertificateInput, _ ...func(*acm.Options)) (*acm.DescribeCertificateOutput, error) {
	status := s.statuses[0]
	if len(s.statuses) > 1 {
		s.statuses = s.statuses[1:]
	}
	return &acm.DescribeCertificateOutput{Certificate: &acmtypes.CertificateDetail{
		CertificateArn: in.CertificateArn,
		DomainName:     aws.String("app.acme.com"),
		Status:         acmtypes.CertificateStatus(status),
		DomainValidationOptions: []acmtypes.DomainValidation{{
			ResourceRecord: &acmtypes.ResourceRecord{
				Name:  aws.String("_ocel.app.acme.com."),
				Type:  acmtypes.RecordTypeCname,
				Value: aws.String("_target.acm-validations.aws."),
			},
		}},
	}}, nil
}

func (s *stubACM) DeleteCertificate(context.Context, *acm.DeleteCertificateInput, ...func(*acm.Options)) (*acm.DeleteCertificateOutput, error) {
	return &acm.DeleteCertificateOutput{}, nil
}

func (s *stubACM) ListCertificates(context.Context, *acm.ListCertificatesInput, ...func(*acm.Options)) (*acm.ListCertificatesOutput, error) {
	return &acm.ListCertificatesOutput{}, nil
}

func issuerOver(api certs.ACMAPI) certs.Issuer {
	return certs.Issuer{
		API:      api,
		Region:   certs.CloudFrontRegion,
		Wait:     func(context.Context, time.Duration) error { return nil },
		Attempts: 2,
		Every:    time.Millisecond,
	}
}

func requestFor(hostname string, held providerkit.Certificate, proved *[]edge.Record) providerkit.CertificateRequest {
	return providerkit.CertificateRequest{
		Kind:     "cloudfront",
		Hostname: hostname,
		Held:     held,
		Report:   silentReporter{},
		Prove: func(_ context.Context, cert providerkit.Certificate, records []edge.Record) (providerkit.Certificate, error) {
			*proved = records
			cert.Written = records
			return cert, nil
		},
	}
}

func TestIssueRequestsACertificateForAnUnpinnedHostname(t *testing.T) {
	t.Parallel()
	api := &stubACM{statuses: []string{certs.StatusPendingValidation, certs.StatusIssued}}
	var proved []edge.Record

	cert, err := issue(context.Background(), issuerOver(api), requestFor("app.acme.com", providerkit.Certificate{}, &proved))
	if err != nil {
		t.Fatalf("issue() error = %v", err)
	}
	if api.requested != 1 {
		t.Errorf("ACM was asked for %d certificates, want exactly one requested", api.requested)
	}
	if cert.ID != issuedARN || !cert.Requested {
		t.Errorf("issue() = %+v, want the handle ocel requested, marked as ocel's", cert)
	}
	if len(proved) != 1 || proved[0].Name != "_ocel.app.acme.com" {
		t.Errorf("the validation records settled were %v, want the one ACM named", proved)
	}
	if len(cert.Written) != 1 {
		t.Errorf("issue() = %+v, want it to carry the validation records the kit wrote", cert)
	}
}

func TestIssueRefusesAsNotReadyWhileACMIsStillValidating(t *testing.T) {
	t.Parallel()
	api := &stubACM{statuses: []string{certs.StatusPendingValidation}}
	var proved []edge.Record

	cert, err := issue(context.Background(), issuerOver(api), requestFor("app.acme.com", providerkit.Certificate{}, &proved))
	var refusal providerkit.Refusal
	if !errors.As(err, &refusal) || refusal.Code != providerkit.CodeNotReady {
		t.Fatalf("issue() error = %v, want the run told to come back to it", err)
	}
	if cert.ID != issuedARN {
		t.Errorf("issue() = %+v, want the requested handle carried out so the re-run picks it up", cert)
	}
	if len(proved) != 1 {
		t.Errorf("the validation records settled were %v, want them written before the wait", proved)
	}
}

func TestIssueKeepsACertificateThatStillCoversTheHostname(t *testing.T) {
	t.Parallel()
	api := &stubACM{statuses: []string{certs.StatusIssued}}
	var proved []edge.Record
	held := providerkit.Certificate{ID: issuedARN, Requested: true}

	cert, err := issue(context.Background(), issuerOver(api), requestFor("app.acme.com", held, &proved))
	if err != nil {
		t.Fatalf("issue() error = %v", err)
	}
	if cert.ID != held.ID || !cert.Requested {
		t.Errorf("issue() = %+v, want the certificate already held", cert)
	}
	if api.requested != 0 {
		t.Errorf("ACM was asked for %d certificates, want none while the held one still covers", api.requested)
	}
}

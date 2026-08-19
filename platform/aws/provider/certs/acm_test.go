package certs

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/acm"
	acmtypes "github.com/aws/aws-sdk-go-v2/service/acm/types"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const testARN = "arn:aws:acm:us-east-1:111122223333:certificate/abcd-1234"

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
	domains     []string
	requested   []*acm.RequestCertificateInput
	minted      map[string]string
	deleted     []string
	listed      [][]acmtypes.CertificateSummary
	lists       int
	describes   int
	requestErr  error
	describeErr error
	deleteErr   error
	listErr     error
}

func (f *fakeACM) RequestCertificate(_ context.Context, in *acm.RequestCertificateInput, _ ...func(*acm.Options)) (*acm.RequestCertificateOutput, error) {
	if f.requestErr != nil {
		return nil, f.requestErr
	}
	f.requested = append(f.requested, in)
	if f.minted == nil {
		f.minted = map[string]string{}
	}
	token := aws.ToString(in.IdempotencyToken)
	arn, ok := f.minted[token]
	if !ok {
		arn = testARN
		if len(f.minted) > 0 {
			arn = fmt.Sprintf("%s-%d", testARN, len(f.minted)+1)
		}
		f.minted[token] = arn
	}
	return &acm.RequestCertificateOutput{CertificateArn: aws.String(arn)}, nil
}

func (f *fakeACM) DescribeCertificate(_ context.Context, in *acm.DescribeCertificateInput, _ ...func(*acm.Options)) (*acm.DescribeCertificateOutput, error) {
	f.describes++
	if f.describeErr != nil && f.describes == 1 {
		return nil, f.describeErr
	}
	step := f.steps[min(f.describes-1, len(f.steps)-1)]
	names := f.domains
	if names == nil {
		names = []string{wildcard}
	}
	out := &acm.DescribeCertificateOutput{Certificate: &acmtypes.CertificateDetail{
		CertificateArn:          in.CertificateArn,
		Status:                  acmtypes.CertificateStatus(step.status),
		DomainName:              aws.String(names[0]),
		SubjectAlternativeNames: names[1:],
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
	if f.deleteErr != nil {
		return nil, f.deleteErr
	}
	f.deleted = append(f.deleted, aws.ToString(in.CertificateArn))
	return &acm.DeleteCertificateOutput{}, nil
}

func (f *fakeACM) ListCertificates(_ context.Context, in *acm.ListCertificatesInput, _ ...func(*acm.Options)) (*acm.ListCertificatesOutput, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	page := f.lists
	f.lists++
	out := &acm.ListCertificatesOutput{}
	if page < len(f.listed) {
		out.CertificateSummaryList = f.listed[page]
	}
	if page+1 < len(f.listed) {
		out.NextToken = aws.String(fmt.Sprintf("page-%d", page+1))
	}
	return out, nil
}

func testIssuer(api ACMAPI, attempts int) Issuer {
	return Issuer{
		API:      api,
		Region:   CloudFrontRegion,
		Wait:     func(context.Context, time.Duration) error { return nil },
		Attempts: attempts,
		Every:    time.Millisecond,
	}
}

func TestIssuerRequest(t *testing.T) {
	t.Parallel()

	t.Run("asks for DNS validation under a token derived from the hostname", func(t *testing.T) {
		t.Parallel()

		api := &fakeACM{}
		cert, err := testIssuer(api, 1).Request(t.Context(), []string{"*.preview.acme.com"})
		if err != nil {
			t.Fatalf("Request: %v", err)
		}
		if cert.ARN != testARN || cert.Region != CloudFrontRegion || cert.Status != StatusPendingValidation {
			t.Fatalf("cert = %+v", cert)
		}
		in := api.requested[0]
		if in.ValidationMethod != acmtypes.ValidationMethodDns {
			t.Errorf("validation method = %v, want DNS", in.ValidationMethod)
		}
		token := aws.ToString(in.IdempotencyToken)
		if len(token) > 32 {
			t.Errorf("idempotency token = %q, %d characters; ACM caps at 32", token, len(token))
		}

		again, _ := testIssuer(api, 1).Request(t.Context(), []string{"*.preview.acme.com"})
		if again.ARN != cert.ARN {
			t.Error("a re-request under the same hostname did not reuse the certificate")
		}
		other, _ := testIssuer(api, 1).Request(t.Context(), []string{"*.preview.other.com"})
		if other.ARN == cert.ARN {
			t.Error("a different hostname was answered with the same certificate")
		}
		if token != idempotencyToken("*.preview.acme.com") {
			t.Error("the token is not derived from the hostname, so a lost response mints a second certificate")
		}
	})

	t.Run("leaves the alternate names unset when there is only one hostname", func(t *testing.T) {
		t.Parallel()

		api := &fakeACM{}
		if _, err := testIssuer(api, 1).Request(t.Context(), []string{"*.preview.acme.com"}); err != nil {
			t.Fatalf("Request: %v", err)
		}
		if api.requested[0].SubjectAlternativeNames != nil {
			t.Errorf("alternate names = %#v, want nil; ACM refuses an empty list", api.requested[0].SubjectAlternativeNames)
		}
	})

	t.Run("a refusal names the hostname and the region", func(t *testing.T) {
		t.Parallel()

		api := &fakeACM{requestErr: errors.New("quota exceeded")}
		_, err := testIssuer(api, 1).Request(t.Context(), []string{"*.preview.acme.com"})
		if err == nil {
			t.Fatal("Request err = nil, want the refusal surfaced")
		}
		for _, want := range []string{"*.preview.acme.com", CloudFrontRegion, "quota exceeded"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("err = %v, want it to contain %q", err, want)
			}
		}
	})
}

func TestIssuerAwaitValidation(t *testing.T) {
	t.Parallel()

	t.Run("waits for ACM to publish the record", func(t *testing.T) {
		t.Parallel()

		api := &fakeACM{steps: []acmStep{
			{status: StatusPendingValidation},
			{status: StatusPendingValidation, validation: true},
		}}
		var said []string
		cert, err := testIssuer(api, 5).AwaitValidation(t.Context(), Certificate{ARN: testARN}, func(m string) { said = append(said, m) })
		if err != nil {
			t.Fatalf("AwaitValidation: %v", err)
		}
		if len(said) != 1 || !strings.Contains(said[0], "Waiting") {
			t.Errorf("said = %v, want the wait announced once, so the run does not look stuck", said)
		}
		if len(cert.Validation) != 1 || cert.Validation[0] != validationRecord {
			t.Fatalf("validation = %+v, want the trailing dots trimmed off %+v", cert.Validation, validationRecord)
		}
	})

	t.Run("gives up bounded, naming the certificate", func(t *testing.T) {
		t.Parallel()

		api := &fakeACM{steps: []acmStep{{status: StatusPendingValidation}}}
		_, err := testIssuer(api, 3).AwaitValidation(t.Context(), Certificate{ARN: testARN}, func(string) {})
		if err == nil {
			t.Fatal("AwaitValidation err = nil, want a bounded refusal")
		}
		if !strings.Contains(err.Error(), testARN) {
			t.Errorf("err = %v, want it to name the certificate", err)
		}
		if api.describes != 3 {
			t.Errorf("describes = %d, want the wait bounded at 3 attempts", api.describes)
		}
	})
}

func TestIssuerAwaitIssued(t *testing.T) {
	t.Parallel()

	cert := Certificate{ARN: testARN, Status: StatusPendingValidation, Validation: []edge.Record{validationRecord}}

	t.Run("returns once ACM says issued", func(t *testing.T) {
		t.Parallel()

		api := &fakeACM{steps: []acmStep{
			{status: StatusPendingValidation, validation: true},
			{status: StatusIssued, validation: true},
		}}
		var said []string
		got, err := testIssuer(api, 5).AwaitIssued(t.Context(), cert, func(m string) { said = append(said, m) })
		if err != nil {
			t.Fatalf("AwaitIssued: %v", err)
		}
		if !got.Issued() {
			t.Fatalf("cert = %+v, want it issued", got)
		}
		if len(said) != 2 || !strings.Contains(said[0], "Waiting") {
			t.Errorf("said = %v, want the wait announced before the issue, so the run does not look stuck", said)
		}
	})

	t.Run("says nothing about waiting when ACM has already issued", func(t *testing.T) {
		t.Parallel()

		api := &fakeACM{steps: []acmStep{{status: StatusIssued, validation: true}}}
		var said []string
		if _, err := testIssuer(api, 5).AwaitIssued(t.Context(), cert, func(m string) { said = append(said, m) }); err != nil {
			t.Fatalf("AwaitIssued: %v", err)
		}
		for _, line := range said {
			if strings.Contains(line, "Waiting") {
				t.Errorf("said = %v, want no wait announced: nothing was waited on", said)
			}
		}
	})

	t.Run("a terminal status stops the wait", func(t *testing.T) {
		t.Parallel()

		api := &fakeACM{steps: []acmStep{{status: string(acmtypes.CertificateStatusFailed)}}}
		_, err := testIssuer(api, 10).AwaitIssued(t.Context(), cert, func(string) {})
		if err == nil {
			t.Fatal("AwaitIssued err = nil, want the failure surfaced")
		}
		if api.describes != 1 {
			t.Errorf("describes = %d, want the wait abandoned on the first terminal status", api.describes)
		}
	})

	t.Run("a timeout prints what is still outstanding", func(t *testing.T) {
		t.Parallel()

		api := &fakeACM{steps: []acmStep{{status: StatusPendingValidation, validation: true}}}
		_, err := testIssuer(api, 2).AwaitIssued(t.Context(), cert, func(string) {})
		if err == nil {
			t.Fatal("AwaitIssued err = nil, want a bounded refusal")
		}
		for _, want := range []string{testARN, validationRecord.Name, validationRecord.Value} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("err = %v, want it to name %q", err, want)
			}
		}
	})
}

func TestIssuerDiscard(t *testing.T) {
	t.Parallel()

	t.Run("deletes the recorded certificate", func(t *testing.T) {
		t.Parallel()

		api := &fakeACM{}
		if err := testIssuer(api, 1).Discard(t.Context(), Certificate{ARN: testARN}, func(string) {}); err != nil {
			t.Fatalf("Discard: %v", err)
		}
		if len(api.deleted) != 1 || api.deleted[0] != testARN {
			t.Errorf("deleted = %v, want %q", api.deleted, testARN)
		}
	})

	t.Run("a certificate ocel adopted is left standing", func(t *testing.T) {
		t.Parallel()

		api := &fakeACM{}
		var said []string
		if err := testIssuer(api, 1).Discard(t.Context(), Certificate{ARN: testARN, Adopted: true}, func(m string) { said = append(said, m) }); err != nil {
			t.Fatalf("Discard: %v", err)
		}
		if len(api.deleted) != 0 {
			t.Errorf("deleted = %v, want a certificate ocel adopted left alone", api.deleted)
		}
		if len(said) != 1 || !strings.Contains(said[0], testARN) {
			t.Errorf("said = %v, want the certificate left standing named", said)
		}
	})

	t.Run("a certificate still in use is named, not fatal", func(t *testing.T) {
		t.Parallel()

		api := &fakeACM{deleteErr: errors.New("ResourceInUseException")}
		var said []string
		err := testIssuer(api, 1).Discard(t.Context(), Certificate{ARN: testARN}, func(m string) { said = append(said, m) })
		if err == nil {
			t.Fatal("Discard err = nil, want the refusal reported so nothing forgets the certificate")
		}
		if len(said) != 1 || !strings.Contains(said[0], testARN) {
			t.Errorf("said = %v, want the certificate left standing named", said)
		}
	})

	t.Run("an edge that needs no certificate has nothing to delete", func(t *testing.T) {
		t.Parallel()

		if err := (Issuer{}).Discard(t.Context(), Certificate{ARN: testARN}, func(string) {
			t.Error("said something with no ACM client in hand")
		}); err != nil {
			t.Fatalf("Discard: %v", err)
		}
	})
}

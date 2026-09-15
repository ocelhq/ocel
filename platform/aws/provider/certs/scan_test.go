package certs

import (
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	acmtypes "github.com/aws/aws-sdk-go-v2/service/acm/types"
)

func TestExistingStopsLoudlyRatherThanMintADuplicatePastTheScan(t *testing.T) {
	t.Parallel()

	listed := make([][]acmtypes.CertificateSummary, certificatePages+1)
	for page := range listed {
		listed[page] = []acmtypes.CertificateSummary{{CertificateArn: aws.String("arn-other"), DomainName: aws.String("other.com")}}
	}
	api := &fakeACM{listed: listed}

	_, err := testIssuer(api, 1).Existing(t.Context(), []string{"app.com"})
	if err == nil {
		t.Fatal("Existing = nil past the scan, want a refusal: silently reporting nothing found would mint a duplicate of a certificate further down")
	}
	if !strings.Contains(err.Error(), "app.com") || !strings.Contains(err.Error(), "certificates") {
		t.Errorf("err = %q, want it to name the hostname and the pin that sidesteps the scan", err)
	}
	if api.lists != certificatePages {
		t.Errorf("listed %d pages, want the scan's %d and no more", api.lists, certificatePages)
	}
}

func TestExistingReadsEveryPageUpToTheScan(t *testing.T) {
	t.Parallel()

	listed := make([][]acmtypes.CertificateSummary, certificatePages)
	for page := range listed {
		listed[page] = []acmtypes.CertificateSummary{{CertificateArn: aws.String("arn-other"), DomainName: aws.String("other.com")}}
	}
	listed[certificatePages-1] = append(listed[certificatePages-1], acmtypes.CertificateSummary{CertificateArn: aws.String("arn-last"), DomainName: aws.String("app.com")})
	api := &fakeACM{listed: listed}

	cert, err := testIssuer(api, 1).Existing(t.Context(), []string{"app.com"})
	if err != nil {
		t.Fatalf("Existing: %v", err)
	}
	if cert.ARN != "arn-last" {
		t.Errorf("cert = %+v, want the certificate on the last page the scan reads", cert)
	}
}

package certs

import (
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	acmtypes "github.com/aws/aws-sdk-go-v2/service/acm/types"

	"github.com/ocelhq/ocel/pkg/environment"
)

func TestCertificateCovers(t *testing.T) {
	t.Parallel()

	cert := Certificate{Domains: []string{"app.com", "*.shop.app.com"}}
	for host, want := range map[string]bool{
		"app.com":           true,
		"APP.com":           true,
		"eu.shop.app.com":   true,
		"shop.app.com":      false,
		"a.b.shop.app.com":  false,
		"www.app.com":       false,
		"shop.app.com.evil": false,
	} {
		if got := cert.Covers(host); got != want {
			t.Errorf("Covers(%q) = %v, want %v", host, got, want)
		}
	}
	if cert.CoversAll([]string{"app.com", "www.app.com"}) {
		t.Error("CoversAll accepted a host the certificate does not name")
	}
}

func TestACMExisting(t *testing.T) {
	t.Parallel()

	t.Run("finds an issued certificate covering every host", func(t *testing.T) {
		t.Parallel()

		api := &fakeACM{listed: [][]acmtypes.CertificateSummary{
			{{CertificateArn: aws.String("arn-1"), DomainName: aws.String("other.com")}},
			{{
				CertificateArn:                  aws.String("arn-2"),
				DomainName:                      aws.String("app.com"),
				SubjectAlternativeNameSummaries: []string{"www.app.com"},
			}},
		}}
		cert, err := testIssuer(api, 1).Existing(t.Context(), []string{"app.com", "www.app.com"})
		if err != nil {
			t.Fatalf("Existing: %v", err)
		}
		if cert.ARN != "arn-2" || !cert.Adopted {
			t.Errorf("cert = %+v, want the certificate already in ACM, unclaimed", cert)
		}
	})

	t.Run("nothing covering is nothing found", func(t *testing.T) {
		t.Parallel()

		api := &fakeACM{listed: [][]acmtypes.CertificateSummary{
			{{CertificateArn: aws.String("arn-1"), DomainName: aws.String("other.com")}},
		}}
		cert, err := testIssuer(api, 1).Existing(t.Context(), []string{"app.com"})
		if err != nil {
			t.Fatalf("Existing: %v", err)
		}
		if cert.ARN != "" {
			t.Errorf("cert = %+v, want nothing found", cert)
		}
	})

	t.Run("a summary that hides its extra names is described", func(t *testing.T) {
		t.Parallel()

		api := &fakeACM{
			steps: []acmStep{{status: StatusIssued}},
			listed: [][]acmtypes.CertificateSummary{{{
				CertificateArn:                       aws.String(testARN),
				DomainName:                           aws.String("app.com"),
				HasAdditionalSubjectAlternativeNames: aws.Bool(true),
			}}},
		}
		if _, err := testIssuer(api, 1).Existing(t.Context(), []string{"www.app.com"}); err != nil {
			t.Fatalf("Existing: %v", err)
		}
		if api.describes != 1 {
			t.Errorf("describes = %d, want the hidden names read", api.describes)
		}
	})
}

func TestExistingAdoptsOnlyACertificateItsOwnTierRequestedOrTheUserBrought(t *testing.T) {
	t.Parallel()

	t.Run("the other tier's certificate and one it may not read are passed over for one the user brought", func(t *testing.T) {
		t.Parallel()

		api := &fakeACM{
			listed: [][]acmtypes.CertificateSummary{{
				{CertificateArn: aws.String("arn-production"), DomainName: aws.String("*.app.com")},
				{CertificateArn: aws.String("arn-unreadable"), DomainName: aws.String("pr-1.app.com")},
				{CertificateArn: aws.String("arn-unreadable-hidden-names"), DomainName: aws.String("app.com"), HasAdditionalSubjectAlternativeNames: aws.Bool(true)},
				{CertificateArn: aws.String("arn-brought"), DomainName: aws.String("*.app.com")},
			}},
			tagged:  map[string]environment.Tier{"arn-production": environment.TierProduction},
			refused: map[string]bool{"arn-unreadable": true, "arn-unreadable-hidden-names": true},
		}
		cert, err := testIssuer(api, 1).Existing(t.Context(), []string{"pr-1.app.com"})
		if err != nil {
			t.Fatalf("Existing: %v", err)
		}
		if cert.ARN != "arn-brought" {
			t.Errorf("a preview deploy adopted %q, want arn-brought: a certificate production requested is production's alone", cert.ARN)
		}
	})

	t.Run("a certificate its own tier requested is adopted", func(t *testing.T) {
		t.Parallel()

		api := &fakeACM{
			listed: [][]acmtypes.CertificateSummary{{
				{CertificateArn: aws.String("arn-production"), DomainName: aws.String("*.app.com")},
				{CertificateArn: aws.String("arn-preview"), DomainName: aws.String("*.app.com")},
			}},
			tagged: map[string]environment.Tier{
				"arn-production": environment.TierProduction,
				"arn-preview":    environment.TierPreview,
			},
		}
		cert, err := testIssuer(api, 1).Existing(t.Context(), []string{"pr-1.app.com"})
		if err != nil {
			t.Fatalf("Existing: %v", err)
		}
		if cert.ARN != "arn-preview" {
			t.Errorf("a preview deploy adopted %q, want arn-preview, the one preview requested", cert.ARN)
		}
	})

	t.Run("only the other tier's certificate covering is nothing found", func(t *testing.T) {
		t.Parallel()

		api := &fakeACM{
			listed: [][]acmtypes.CertificateSummary{{
				{CertificateArn: aws.String("arn-production"), DomainName: aws.String("*.app.com")},
			}},
			tagged: map[string]environment.Tier{"arn-production": environment.TierProduction},
		}
		cert, err := testIssuer(api, 1).Existing(t.Context(), []string{"pr-1.app.com"})
		if err != nil {
			t.Fatalf("Existing: %v", err)
		}
		if cert.ARN != "" {
			t.Errorf("a preview deploy adopted %q, want nothing found so it requests its own", cert.ARN)
		}
	})
}

func TestIssuerPinned(t *testing.T) {
	t.Parallel()

	t.Run("a certificate in another region is refused before it is read", func(t *testing.T) {
		t.Parallel()

		api := &fakeACM{}
		_, err := testIssuer(api, 1).Pinned(t.Context(), "app.com", "arn:aws:acm:eu-west-1:1:certificate/x")
		if err == nil {
			t.Fatal("Pinned err = nil, want a refusal")
		}
		if api.describes != 0 {
			t.Error("a certificate in the wrong region was read anyway")
		}
		for _, want := range []string{"eu-west-1", CloudFrontRegion} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("err = %v, want it to name %q", err, want)
			}
		}
	})

	t.Run("a certificate that is not issued is refused", func(t *testing.T) {
		t.Parallel()

		api := &fakeACM{steps: []acmStep{{status: StatusPendingValidation}}}
		_, err := testIssuer(api, 1).Pinned(t.Context(), "app.com", testARN)
		if err == nil {
			t.Fatal("Pinned err = nil, want a refusal while the pin is still validating")
		}
		if !strings.Contains(err.Error(), "never issued or deleted") {
			t.Errorf("err = %v, want it to say ocel leaves a pinned certificate alone", err)
		}
	})
}

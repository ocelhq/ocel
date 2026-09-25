package certs

import (
	"net/http"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
)

func TestIssuerFor(t *testing.T) {
	t.Parallel()

	uncertified := ACMFor("", Deps{AWS: aws.Config{Region: "eu-west-2"}})
	if uncertified.API != nil {
		t.Error("an edge that certifies nothing was handed an ACM client; it terminates TLS itself")
	}

	pinned := ACMFor(CloudFrontRegion, Deps{AWS: aws.Config{Region: "eu-west-2"}})
	if pinned.API == nil || pinned.Region != CloudFrontRegion {
		t.Errorf("acm = %+v, want an ACM client in %s", pinned, CloudFrontRegion)
	}
}

func TestDiscardIssuerFor(t *testing.T) {
	t.Parallel()

	deps := Deps{AWS: aws.Config{Region: "eu-west-2"}}

	t.Run("deletes in the region the certificate was issued in", func(t *testing.T) {
		t.Parallel()

		acm := DiscardACMFor(Certificate{ARN: testARN, Region: CloudFrontRegion}, deps)
		if acm.API == nil || acm.Region != CloudFrontRegion {
			t.Errorf("acm = %+v, want an ACM client in %s, whatever edge is in front now", acm, CloudFrontRegion)
		}
	})

	t.Run("nothing recorded is nothing to delete", func(t *testing.T) {
		t.Parallel()

		if acm := DiscardACMFor(Certificate{}, deps); acm.API != nil {
			t.Errorf("acm = %+v, want no client for a certificate that was never issued", acm)
		}
	})
}

func TestACMHTTPClient(t *testing.T) {
	t.Parallel()

	if got := (Deps{}).http(); got.Timeout <= 0 {
		t.Errorf("timeout = %s, want the ACM calls bounded", got.Timeout)
	}
	mine := &http.Client{Timeout: time.Second}
	if got := (Deps{HTTP: mine}).http(); got != mine {
		t.Error("an injected client was ignored")
	}
}

package certs

import (
	"net/http"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func TestRegionFor(t *testing.T) {
	t.Parallel()

	for kind, want := range map[edge.Kind]string{
		edge.KindCloudflare: "",
		edge.KindNative:     CloudFrontRegion,
		edge.KindNone:       "eu-west-2",
		"fastly":            "",
	} {
		if got := RegionFor(kind, "eu-west-2"); got != want {
			t.Errorf("RegionFor(%s) = %q, want %q", kind, got, want)
		}
	}
}

func TestIssuerFor(t *testing.T) {
	t.Parallel()

	cloudflare := IssuerFor(edge.KindCloudflare, Deps{AWS: aws.Config{Region: "eu-west-2"}})
	if cloudflare.API != nil {
		t.Error("cloudflare was handed an ACM client; it terminates TLS itself")
	}

	native := IssuerFor(edge.KindNative, Deps{AWS: aws.Config{Region: "eu-west-2"}})
	if native.API == nil || native.Region != CloudFrontRegion {
		t.Errorf("issuer = %+v, want an ACM client in %s", native, CloudFrontRegion)
	}
}

func TestDiscardIssuerFor(t *testing.T) {
	t.Parallel()

	deps := Deps{AWS: aws.Config{Region: "eu-west-2"}}

	t.Run("deletes in the region the certificate was issued in", func(t *testing.T) {
		t.Parallel()

		issuer := DiscardIssuerFor(Certificate{ARN: testARN, Region: CloudFrontRegion}, deps)
		if issuer.API == nil || issuer.Region != CloudFrontRegion {
			t.Errorf("issuer = %+v, want an ACM client in %s, whatever edge is in front now", issuer, CloudFrontRegion)
		}
	})

	t.Run("nothing recorded is nothing to delete", func(t *testing.T) {
		t.Parallel()

		if issuer := DiscardIssuerFor(Certificate{}, deps); issuer.API != nil {
			t.Errorf("issuer = %+v, want no client for a certificate that was never issued", issuer)
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

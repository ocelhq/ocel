package certs

import (
	"net/http"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	unboundEdgeKind      edge.Kind = "unbound-edge"
	frontedEdgeKind      edge.Kind = "fronted-edge"
	otherFrontedEdgeKind edge.Kind = "other-fronted-edge"
)

type uncertifiedEdge struct{ edge.Edge }

type certifyingEdge struct {
	edge.Edge
	region string
}

func (c certifyingEdge) CertificateRegion(apiRegion string) string {
	if c.region == "" {
		return apiRegion
	}
	return c.region
}

func TestRegionFor(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		front edge.Edge
		want  string
	}{
		"an edge that certifies nothing is handed no region": {uncertifiedEdge{}, ""},
		"an edge that pins a region takes it":                {certifyingEdge{region: CloudFrontRegion}, CloudFrontRegion},
		"an edge that certifies where the API lives":         {certifyingEdge{}, "eu-west-2"},
	} {
		if got := RegionFor(tc.front, "eu-west-2"); got != tc.want {
			t.Errorf("%s: RegionFor = %q, want %q", name, got, tc.want)
		}
	}
}

func TestIssuerFor(t *testing.T) {
	t.Parallel()

	uncertified := IssuerFor(uncertifiedEdge{}, Deps{AWS: aws.Config{Region: "eu-west-2"}})
	if uncertified.API != nil {
		t.Error("an edge that certifies nothing was handed an ACM client; it terminates TLS itself")
	}

	pinned := IssuerFor(certifyingEdge{region: CloudFrontRegion}, Deps{AWS: aws.Config{Region: "eu-west-2"}})
	if pinned.API == nil || pinned.Region != CloudFrontRegion {
		t.Errorf("issuer = %+v, want an ACM client in %s", pinned, CloudFrontRegion)
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

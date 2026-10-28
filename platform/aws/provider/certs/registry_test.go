package certs

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awsacm "github.com/aws/aws-sdk-go-v2/service/acm"

	"github.com/ocelhq/ocel/pkg/tracing"
	"github.com/ocelhq/ocel/pkg/tracing/tracingtest"
)

func TestIssuerFor(t *testing.T) {
	t.Parallel()

	uncertified := ACMFor("", Deps{AWS: aws.Config{Region: "eu-west-2"}})
	if uncertified.API != nil {
		t.Error("an edge that certifies nothing was handed an ACM client; it terminates TLS itself")
	}

	pinned := ACMFor(CloudFrontRegion, Deps{AWS: aws.Config{Region: "eu-west-2"}, Namespace: "acme"})
	if pinned.API == nil || pinned.Region != CloudFrontRegion {
		t.Errorf("acm = %+v, want an ACM client in %s", pinned, CloudFrontRegion)
	}
	if pinned.ManagedBy != "acme" {
		t.Errorf("acm tags what it requests as managed by %q, want acme, the namespace it serves", pinned.ManagedBy)
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

func TestAnACMCallsHTTPExchangeIsOneSpanWhetherOrNotItsClientIsTraced(t *testing.T) {
	for name, client := range map[string]*http.Client{
		"the default client": nil,
		"a traced client":    {Transport: tracing.NewTransport(nil)},
	} {
		t.Run(name, func(t *testing.T) {
			ctx, recorded := tracingtest.Record(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/x-amz-json-1.1")
				_, _ = io.WriteString(w, `{"CertificateSummaryList":[]}`)
			}))
			t.Cleanup(server.Close)

			acm := ACMFor(CloudFrontRegion, Deps{HTTP: client, AWS: aws.Config{
				Region:       "eu-west-2",
				BaseEndpoint: aws.String(server.URL),
				Credentials:  credentials.NewStaticCredentialsProvider("AKIDEXAMPLE", "example-secret", ""),
			}})
			if _, err := acm.API.ListCertificates(ctx, &awsacm.ListCertificatesInput{}); err != nil {
				t.Fatalf("ListCertificates: %v", err)
			}

			exchanges := 0
			for _, s := range recorded.Ended() {
				if s.InstrumentationScope().Name == tracing.Scope {
					exchanges++
				}
			}
			if exchanges != 1 {
				t.Errorf("one ListCertificates call made %d HTTP spans, want 1", exchanges)
			}
		})
	}
}

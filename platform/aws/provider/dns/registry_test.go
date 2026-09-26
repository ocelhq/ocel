package dns

import (
	"errors"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit/conformance"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func TestRegistryConformance(t *testing.T) {
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "conformance")

	conformance.RunDNS(t, provider.Facts{DNSKinds: Kinds()}, Registry{})
}

func TestKinds(t *testing.T) {
	t.Parallel()

	if got := Kinds(); !slices.Equal(got, []provider.DNSKind{provider.DNSKind(KindCloudflare), provider.DNSKind(KindRoute53)}) {
		t.Errorf("Kinds() = %v, want cloudflare and route53", got)
	}
}

func TestRecordsForNamesNoWriterWhenNoneIsAsked(t *testing.T) {
	t.Parallel()

	writer, err := RecordsFor("", "acme.com", Deps{})
	if err != nil {
		t.Fatalf("RecordsFor(\"\") error = %v", err)
	}
	if writer != nil {
		t.Errorf("RecordsFor(\"\") = %v, want no writer: a request that names none owes the operator its records", writer)
	}
}

func TestRecordsForRefusesAnUnknownKind(t *testing.T) {
	t.Parallel()

	writer, err := RecordsFor("bogus", "acme.com", Deps{})
	if err == nil {
		t.Fatalf("RecordsFor(bogus) = %v, want a refusal", writer)
	}
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
		t.Fatalf("RecordsFor(bogus) error = %v, want a %s refusal", err, refusal.CodeInvalid)
	}
}

func TestRegistryRefusesRoute53UnderACloudflareEdge(t *testing.T) {
	t.Parallel()

	writer, err := Registry{}.Open(KindRoute53, "acme.com", cloudflare.Kind)
	if err == nil {
		t.Fatalf("Open(route53) under a cloudflare edge = %v, want a refusal", writer)
	}
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
		t.Fatalf("Open(route53) under a cloudflare edge error = %v, want a %s refusal", err, refusal.CodeInvalid)
	}
	want := "route53 cannot write the records a Cloudflare edge answers on — pair a cloudflare edge with cloudflare dns, or drop the edge"
	if refused.Message != want {
		t.Fatalf("refusal = %q, want %q", refused.Message, want)
	}
}

func TestRegistryOpensRoute53UnderAnyOtherEdge(t *testing.T) {
	t.Parallel()

	for _, front := range []edge.Kind{"", "cloudfront", "api-gateway"} {
		if writer, err := (Registry{}).Open(KindRoute53, "acme.com", front); err != nil || writer == nil {
			t.Errorf("Open(route53) under edge %q = %v, %v, want a writer", front, writer, err)
		}
	}
}

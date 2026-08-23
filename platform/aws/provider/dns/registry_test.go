package dns

import (
	"errors"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/conformance"
	kit "github.com/ocelhq/ocel/pkg/providerkit/ports"
)

func TestRegistryConformance(t *testing.T) {
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "conformance")

	conformance.RunDNSRegistry(t, Registry{})
}

func TestRegistrySupportedKinds(t *testing.T) {
	t.Parallel()

	if got := (Registry{}).Supported(); !slices.Equal(got, []providerkit.DNSKind{providerkit.DNSKind(KindCloudflare), providerkit.DNSKind(KindRoute53)}) {
		t.Errorf("Supported() = %v, want cloudflare and route53", got)
	}
}

func TestWriterForNamesNoWriterWhenNoneIsAsked(t *testing.T) {
	t.Parallel()

	writer, err := WriterFor("", "acme.com", Deps{})
	if err != nil {
		t.Fatalf("WriterFor(\"\") error = %v", err)
	}
	if writer != nil {
		t.Errorf("WriterFor(\"\") = %v, want no writer: a request that names none owes the operator its records", writer)
	}
}

func TestWriterForRefusesAnUnknownKind(t *testing.T) {
	t.Parallel()

	writer, err := WriterFor("bogus", "acme.com", Deps{})
	if err == nil {
		t.Fatalf("WriterFor(bogus) = %v, want a refusal", writer)
	}
	var refusal kit.Refusal
	if !errors.As(err, &refusal) || refusal.Code != kit.CodeInvalid {
		t.Fatalf("WriterFor(bogus) error = %v, want a %s refusal", err, kit.CodeInvalid)
	}
}

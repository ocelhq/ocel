package alb

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

func TestAWildcardAndTheDomainUnderneathItGetNamesOfTheirOwn(t *testing.T) {
	t.Parallel()

	class := providerkit.ClassProduction
	for _, named := range []struct {
		what string
		name func(string, providerkit.Class, string) string
	}{
		{"backend service", backendName},
		{"certificate map entry", entryName},
		{"serverless neg", negName},
	} {
		under := named.name("shop", class, "preview.example.com")
		wildcard := named.name("shop", class, "*.preview.example.com")
		if under == wildcard {
			t.Errorf("the %s for preview.example.com and for *.preview.example.com is %q for both, and the second bind would take over the first's",
				named.what, under)
		}
	}
}

func TestAHostnameTooLongToNameIsShortenedRatherThanRefusedByCompute(t *testing.T) {
	t.Parallel()

	hostname := "checkout-eu-west-staging.a-very-long-customer-subdomain.example.com"
	class := providerkit.ClassProduction
	for _, named := range []struct {
		what string
		name func(string, providerkit.Class, string) string
	}{
		{"backend service", backendName},
		{"certificate map entry", entryName},
		{"serverless neg", negName},
	} {
		got := named.name("a-long-project-slug", class, hostname)
		if len(got) > maxResourceName {
			t.Errorf("the %s is named %q, %d characters: Compute refuses a name over %d", named.what, got, len(got), maxResourceName)
		}
	}
}

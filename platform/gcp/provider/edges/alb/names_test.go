package alb

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
)

func TestAWildcardAndTheDomainUnderneathItGetNamesOfTheirOwn(t *testing.T) {
	t.Parallel()

	tier := environment.TierProduction
	for _, named := range []struct {
		what string
		name func(string, environment.Tier, string) string
	}{
		{"backend service", backendName},
		{"certificate map entry", entryName},
		{"serverless neg", negName},
	} {
		under := named.name("shop", tier, "preview.example.com")
		wildcard := named.name("shop", tier, "*.preview.example.com")
		if under == wildcard {
			t.Errorf("the %s for preview.example.com and for *.preview.example.com is %q for both, and the second bind would take over the first's",
				named.what, under)
		}
	}
}

func TestAHostnameTooLongToNameIsShortenedRatherThanRefusedByCompute(t *testing.T) {
	t.Parallel()

	hostname := "checkout-eu-west-staging.a-very-long-customer-subdomain.example.com"
	tier := environment.TierProduction
	for _, named := range []struct {
		what string
		name func(string, environment.Tier, string) string
	}{
		{"backend service", backendName},
		{"certificate map entry", entryName},
		{"serverless neg", negName},
	} {
		got := named.name("a-long-project-slug", tier, hostname)
		if len(got) > maxResourceName {
			t.Errorf("the %s is named %q, %d characters: Compute refuses a name over %d", named.what, got, len(got), maxResourceName)
		}
	}
}

func TestTheURLMapPathNamesTheTiersLoadBalancerURLMap(t *testing.T) {
	t.Parallel()

	for tier, want := range map[environment.Tier]string{
		environment.TierProduction: "projects/acme-prod/global/urlMaps/ocel-alb-production-routes",
		environment.TierPreview:    "projects/acme-prod/global/urlMaps/ocel-alb-preview-routes",
	} {
		if got := URLMapPath("acme-prod", tier); got != want {
			t.Errorf("URLMapPath(acme-prod, %s) = %q, want %q", tier, got, want)
		}
	}
}

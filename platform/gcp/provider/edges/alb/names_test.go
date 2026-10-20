package alb

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
)

func TestAWildcardAndTheDomainUnderneathItGetNamesOfTheirOwn(t *testing.T) {
	t.Parallel()

	tier := environment.TierProduction
	for _, named := range []struct {
		what string
		name func(string, string, environment.Tier, string) string
	}{
		{"backend service", backendName},
		{"certificate map entry", entryName},
		{"serverless neg", negName},
	} {
		under := named.name("ocel", "shop", tier, "preview.example.com")
		wildcard := named.name("ocel", "shop", tier, "*.preview.example.com")
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
		name func(string, string, environment.Tier, string) string
	}{
		{"backend service", backendName},
		{"certificate map entry", entryName},
		{"serverless neg", negName},
	} {
		got := named.name("northwind-traders-europe-west", "a-long-project-slug", tier, hostname)
		if len(got) > maxResourceName {
			t.Errorf("the %s is named %q, %d characters: Compute refuses a name over %d", named.what, got, len(got), maxResourceName)
		}
	}
}

func TestAHostsBackendIsNamedUnderTheNamespaceAndTierAndItsEntryAndNEGAppendToIt(t *testing.T) {
	t.Parallel()

	backend := backendName("ocel", "shop", environment.TierProduction, "shop.example.com")
	if !strings.HasPrefix(backend, "ocel-prod-alb-shop-shop-example-com-") {
		t.Errorf("backendName() = %q, want the namespace, the tier word, the edge, the project and the hostname readable at its start", backend)
	}
	if entry := entryName("ocel", "shop", environment.TierProduction, "shop.example.com"); entry != backend+"-cert" {
		t.Errorf("entryName() = %q, want the backend %q and -cert", entry, backend)
	}
	if neg := negName("ocel", "shop", environment.TierProduction, "shop.example.com"); neg != backend+"-neg" {
		t.Errorf("negName() = %q, want the backend %q and -neg", neg, backend)
	}
}

func TestAProjectAndAHostnameThatSplitTheSameLettersAreTwoBackends(t *testing.T) {
	t.Parallel()

	one := backendName("ocel", "shop-eu", environment.TierProduction, "example.com")
	other := backendName("ocel", "shop", environment.TierProduction, "eu.example.com")
	if one == other {
		t.Errorf("both are served by %q, and one project's bind would take over the other's hostname", one)
	}
}

func TestTwoNamespacesNameTheirLoadBalancersApart(t *testing.T) {
	t.Parallel()

	ours, theirs := loadBalancerNames("ocel", environment.TierProduction, false), loadBalancerNames("ocel-dev", environment.TierProduction, false)
	if ours.URLMap == theirs.URLMap || ours.Address == theirs.Address {
		t.Errorf("namespaces ocel and ocel-dev both name their load balancer %q, and the second bootstrap would take over the first's", ours.URLMap)
	}
}

func TestTheURLMapPathNamesTheTiersLoadBalancerURLMap(t *testing.T) {
	t.Parallel()

	for tier, want := range map[environment.Tier]string{
		environment.TierProduction: "projects/acme-prod/global/urlMaps/ocel-prod-alb-routes",
		environment.TierPreview:    "projects/acme-prod/global/urlMaps/ocel-preview-alb-routes",
	} {
		if got := URLMapPath("ocel", "acme-prod", tier); got != want {
			t.Errorf("URLMapPath(ocel, acme-prod, %s) = %q, want %q", tier, got, want)
		}
	}
}

func TestAProjectsSurfaceNamesItsNamespace(t *testing.T) {
	t.Parallel()

	if got, want := Surface("ocel-dev", "shop", environment.TierProduction), "ocel-dev--alb--shop--prod"; got != want {
		t.Errorf("Surface() = %q, want %q", got, want)
	}
}

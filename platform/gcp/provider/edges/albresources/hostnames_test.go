package albresources

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
		name func(string, string, environment.Tier, string) (string, error)
	}{
		{"backend service", BackendName},
		{"certificate map entry", EntryName},
		{"serverless neg", NegName},
	} {
		under := must(named.name("ocel", "shop", tier, "preview.example.com"))
		wildcard := must(named.name("ocel", "shop", tier, "*.preview.example.com"))
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
		name func(string, string, environment.Tier, string) (string, error)
	}{
		{"backend service", BackendName},
		{"certificate map entry", EntryName},
		{"serverless neg", NegName},
	} {
		got := must(named.name("northwind-traders-europe-west", "a-long-project-slug", tier, hostname))
		if len(got) > maxResourceName {
			t.Errorf("the %s is named %q, %d characters: Compute refuses a name over %d", named.what, got, len(got), maxResourceName)
		}
	}
}

func TestEveryStackOfTheLoadBalancerNamesItsTierByItsWord(t *testing.T) {
	t.Parallel()

	for _, named := range []struct {
		target Stack
		want   string
	}{
		{Stack{Tier: environment.TierProduction}, "alb-front-prod"},
		{Stack{Tier: environment.TierPreview}, "alb-front-preview"},
		{Stack{Tier: environment.TierProduction, Gate: true}, "alb-gate-prod"},
		{Stack{Tier: environment.TierProduction, Slug: "shop"}, "alb-prod-shop"},
	} {
		if got := named.target.Name(); got != named.want {
			t.Errorf("%+v.Name() = %q, want %q", named.target, got, named.want)
		}
	}
}

func TestAHostsBackendIsNamedUnderTheNamespaceAndTierAndItsEntryAndNEGAppendToIt(t *testing.T) {
	t.Parallel()

	backend := must(BackendName("ocel", "shop", environment.TierProduction, "shop.example.com"))
	if !strings.HasPrefix(backend, "ocel-prod-alb-shop-shop-example-com-") {
		t.Errorf("BackendName() = %q, want the namespace, the tier word, the edge, the project and the hostname readable at its start", backend)
	}
	if entry := must(EntryName("ocel", "shop", environment.TierProduction, "shop.example.com")); entry != backend+"-cert" {
		t.Errorf("EntryName() = %q, want the backend %q and -cert", entry, backend)
	}
	if neg := must(NegName("ocel", "shop", environment.TierProduction, "shop.example.com")); neg != backend+"-neg" {
		t.Errorf("NegName() = %q, want the backend %q and -neg", neg, backend)
	}
}

func TestAProjectAndAHostnameThatSplitTheSameLettersAreTwoBackends(t *testing.T) {
	t.Parallel()

	one := must(BackendName("ocel", "shop-eu", environment.TierProduction, "example.com"))
	other := must(BackendName("ocel", "shop", environment.TierProduction, "eu.example.com"))
	if one == other {
		t.Errorf("both are served by %q, and one project's bind would take over the other's hostname", one)
	}
}

func must[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}

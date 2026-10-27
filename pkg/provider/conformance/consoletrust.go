package conformance

import (
	"context"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
)

func RunConsoleTrust(t *testing.T, facts provider.Facts, offered []provider.Part) {
	t.Helper()

	t.Run("a provider that deploys with a web identity offers the console's trust as a part, and one that does not offers none", func(t *testing.T) {
		trusts := slices.ContainsFunc(offered, func(part provider.Part) bool { return part.Name == provider.ConsoleTrustPart })
		if facts.WebIdentity != nil && !trusts {
			t.Errorf("the bootstrap offers no %s part, so nothing it deploys with a web identity can be trusted by a console", provider.ConsoleTrustPart)
		}
		if facts.WebIdentity == nil && trusts {
			t.Errorf("the bootstrap offers %s, and the provider deploys with no web identity that trust could issue", provider.ConsoleTrustPart)
		}
	})
}

func RunConsoleTrustBootstrap(t *testing.T, bootstrap provider.Bootstrap, tier environment.Tier) {
	t.Helper()

	t.Run("a bootstrap handed a console's trust says the identity it made, and forgets it once the part is removed", func(t *testing.T) {
		ctx := context.Background()
		trust := &provider.WebIdentityTrust{
			Issuer:         "https://console.example.test/oidc",
			Audience:       "https://console.example.test",
			OrganizationID: "0199d1e2-0000-7000-8000-00000000000a",
			TargetID:       "0199d1e2-0000-7000-8000-00000000000b",
		}
		if err := bootstrap.Apply(ctx, provider.BootstrapRequest{Tier: tier, Parts: []string{provider.ConsoleTrustPart}, Trust: trust}, progress.Discard()); err != nil {
			t.Fatalf("Apply(trust) = %v", err)
		}
		described, err := bootstrap.Describe(ctx, tier)
		if err != nil {
			t.Fatalf("Describe() = %v", err)
		}
		if described.Identity == nil || described.Identity.IdentityProvider == "" || described.Identity.Principal == "" {
			t.Fatalf("Describe().Identity = %+v after a bootstrap trusting the console, want the identity provider and principal it made", described.Identity)
		}
		if described.Trust == nil || described.Trust.TargetID != trust.TargetID || described.Trust.Issuer != trust.Issuer || described.Trust.OrganizationID != trust.OrganizationID {
			t.Errorf("Describe().Trust = %+v, want the trust the part was applied with, %+v", described.Trust, trust)
		}
		if err := bootstrap.Apply(ctx, provider.BootstrapRequest{Tier: tier, Remove: []string{provider.ConsoleTrustPart}}, progress.Discard()); err != nil {
			t.Fatalf("Apply(remove %s) = %v", provider.ConsoleTrustPart, err)
		}
		if described, err = bootstrap.Describe(ctx, tier); err != nil || described.Identity != nil || described.Trust != nil {
			t.Errorf("Describe() = %+v, %+v, %v after the part is removed, want no identity and no trust", described.Identity, described.Trust, err)
		}
	})
}

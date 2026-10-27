package providerserver_test

import (
	"context"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/providerserver"
)

func consoleTrust() *contractv1.WebIdentityTrust {
	return &contractv1.WebIdentityTrust{
		Issuer:         "https://console.example.com/oidc",
		Audience:       "https://console.example.com",
		OrganizationId: "o",
		TargetId:       "t",
		Github:         &contractv1.GitHubTrust{Repository: "acme/shop", Branch: "main"},
	}
}

func TestABootstrapTrustingTheConsoleAddsItsPartAndSaysTheTiersIdentity(t *testing.T) {
	vendor := fake.NewProvider(fake.Options{Region: "nowhere"})
	bootstrap := vendor.FakeBootstrap()
	bootstrap.Offers(append(bootstrap.Parts(), provider.Part{Name: provider.ConsoleTrustPart, Summary: "the console's trust"})...)
	client := servedProvider(t, "1.2.3", vendor)

	stream, err := client.Bootstrap(context.Background(), &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION, Trust: consoleTrust()})
	if err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}
	if _, err := drain(stream); err != nil {
		t.Fatalf("Bootstrap() stream = %v", err)
	}

	applied := bootstrap.Applied()
	last := applied[len(applied)-1]
	want := provider.WebIdentityTrust{
		Issuer: "https://console.example.com/oidc", Audience: "https://console.example.com",
		OrganizationID: "o", TargetID: "t",
		GitHub: &provider.GitHubTrust{Repository: "acme/shop", Branch: "main"},
	}
	if last.Trust == nil || last.Trust.Issuer != want.Issuer || last.Trust.TargetID != want.TargetID || *last.Trust.GitHub != *want.GitHub {
		t.Errorf("the vendor was handed trust %+v, want %+v", last.Trust, want)
	}
	if !slices.Contains(last.Parts, provider.ConsoleTrustPart) {
		t.Errorf("the vendor was asked for parts %v, want %s among them", last.Parts, provider.ConsoleTrustPart)
	}

	described, err := client.DescribeBootstrap(context.Background(), &contractv1.DescribeBootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION})
	if err != nil {
		t.Fatalf("DescribeBootstrap() error = %v", err)
	}
	if got := described.GetBootstrap().GetIdentity(); got.GetPrincipal() != "fake-deploy-production" || got.GetIdentityProvider() != "fake-issuer" {
		t.Errorf("DescribeBootstrap().Identity = %v, want the identity the trust part made", got)
	}
}

func TestABootstrapTrustingTheConsoleIsRefusedByAProviderWithNoTrustPart(t *testing.T) {
	client, _ := contractServed(t, "1.2.3")

	stream, err := client.Bootstrap(context.Background(), &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION, Trust: consoleTrust()})
	if err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}
	result, err := drain(stream)
	if err == nil && (result.GetSuccess() || !strings.Contains(result.GetError(), provider.ConsoleTrustPart)) {
		t.Fatalf("Bootstrap() = %v, want a refusal naming the missing %s part", result, provider.ConsoleTrustPart)
	}
}

func webIdentityServed(t *testing.T, vendor *fake.Provider) contractv1connect.ProviderServiceClient {
	t.Helper()
	server := httptest.NewServer(providerserver.ConformanceMux(providerserver.Config{
		Version: "1.2.3",
		New:     func(context.Context, provider.Settings) (provider.Provider, error) { return vendor, nil },
	}))
	t.Cleanup(server.Close)
	client := contractv1connect.NewProviderServiceClient(server.Client(), server.URL)
	if _, err := client.Configure(context.Background(), &contractv1.ConfigureRequest{Config: &contractv1.ProviderConfig{
		ProjectDir:  t.TempDir(),
		WebIdentity: &contractv1.WebIdentity{TokenFile: "/tmp/token", IdentityProvider: "fake-issuer", Principal: "fake-deploy-production"},
	}}); err != nil {
		t.Fatalf("Configure() error = %v", err)
	}
	return client
}

func TestADeployWithAWebIdentityIsRefusedUntilTheTiersConsoleTrustIsCurrent(t *testing.T) {
	for name, tc := range map[string]struct {
		parts []string
		stale bool
		ready bool
	}{
		"no console trust":        {parts: nil},
		"a stale console trust":   {parts: []string{provider.ConsoleTrustPart}, stale: true},
		"a current console trust": {parts: []string{provider.ConsoleTrustPart}, ready: true},
	} {
		t.Run(name, func(t *testing.T) {
			vendor := takingWebIdentity(fake.NewProvider(fake.Options{Region: "nowhere"}))
			bootstrap := vendor.FakeBootstrap()
			bootstrap.Offers(append(bootstrap.Parts(), provider.Part{Name: provider.ConsoleTrustPart, Summary: "the console's trust"})...)
			client := webIdentityServed(t, vendor)
			bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION, Trust: map[bool]*contractv1.WebIdentityTrust{true: consoleTrust()}[tc.parts != nil]})
			if tc.stale {
				bootstrap.MarkStale(provider.ConsoleTrustPart)
			}

			_, err := preflight(context.Background(), client, &contractv1.PreflightRequest{RequiredTier: environmentv1.Tier_TIER_PRODUCTION})

			if tc.ready && err != nil {
				t.Fatalf("Preflight() = %v, want a deploy identity this ocel bootstrapped to be ready", err)
			}
			if !tc.ready && (err == nil || !strings.Contains(err.Error(), "the production deploy identity predates this ocel; an admin runs `ocel bootstrap production`")) {
				t.Fatalf("Preflight() = %v, want the stale deploy identity refused", err)
			}
		})
	}
}

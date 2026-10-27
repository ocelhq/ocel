package providerserver_test

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/providerserver"
)

func takingWebIdentity(p *fake.Provider) *fake.Provider {
	return p.WithFacts(func(facts *provider.Facts) {
		facts.WebIdentity = &provider.WebIdentityFacts{CredentialEnvVars: []string{"FAKE_ACCESS_KEY", "FAKE_PROFILE"}}
	})
}

func configureServing(t *testing.T, p *fake.Provider, config *contractv1.ProviderConfig) (*provider.Settings, error) {
	t.Helper()

	seen := &provider.Settings{}
	server := httptest.NewServer(providerserver.ConformanceMux(providerserver.Config{
		Version: "test",
		New: func(_ context.Context, settings provider.Settings) (provider.Provider, error) {
			*seen = settings
			return p, nil
		},
	}))
	t.Cleanup(server.Close)
	client := contractv1connect.NewProviderServiceClient(server.Client(), server.URL)
	config.ProjectDir = t.TempDir()
	_, err := client.Configure(context.Background(), &contractv1.ConfigureRequest{Config: config})
	return seen, err
}

func TestConfigureHandsTheProviderTheWebIdentityItDeploysAs(t *testing.T) {
	identity := &contractv1.WebIdentity{
		TokenFile:        "/tmp/ocel-console/token",
		IdentityProvider: "issuer-of-the-console",
		Principal:        "deploy-production",
		SessionName:      "ocel-deploy-1",
	}
	seen, err := configureServing(t, takingWebIdentity(fake.NewProvider(fake.Options{})), &contractv1.ProviderConfig{WebIdentity: identity})
	if err != nil {
		t.Fatalf("Configure() error = %v", err)
	}

	want := provider.WebIdentity{TokenFile: "/tmp/ocel-console/token", IdentityProvider: "issuer-of-the-console", Principal: "deploy-production", SessionName: "ocel-deploy-1"}
	if seen.WebIdentity == nil || *seen.WebIdentity != want {
		t.Errorf("WebIdentity = %+v, want %+v", seen.WebIdentity, want)
	}
}

func TestConfigureRefusesAWebIdentityForAProviderThatTakesNone(t *testing.T) {
	_, err := configureServing(t, fake.NewProvider(fake.Options{}), &contractv1.ProviderConfig{WebIdentity: &contractv1.WebIdentity{
		TokenFile: "/tmp/token", IdentityProvider: "issuer", Principal: "deploy",
	}})
	if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Fatalf("Configure() code = %v (%v), want %v: the provider would deploy with the environment's credentials while the caller thinks it holds the console's", got, err, connect.CodeInvalidArgument)
	}
}

func TestConfigureRefusesAWebIdentityForARunThatNamesItsOwnCredential(t *testing.T) {
	t.Setenv("FAKE_PROFILE", "deployer")

	_, err := configureServing(t, takingWebIdentity(fake.NewProvider(fake.Options{})), &contractv1.ProviderConfig{WebIdentity: &contractv1.WebIdentity{
		TokenFile: "/tmp/token", IdentityProvider: "issuer", Principal: "deploy",
	}})
	if got := connect.CodeOf(err); got != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "FAKE_PROFILE") {
		t.Fatalf("Configure() = %v (%v), want a refusal naming FAKE_PROFILE: which credential deploys would otherwise be the SDK's guess", got, err)
	}
}

func TestConfigureStatesTheWebIdentityTheProviderTakes(t *testing.T) {
	_, facts := configuredProvider(t, "1.2.3", takingWebIdentity(fake.NewProvider(fake.Options{})))

	taken := facts.GetWebIdentity()
	if taken.GetVendor() != string(fake.Vendor) {
		t.Errorf("WebIdentity.Vendor = %q, want %q", taken.GetVendor(), fake.Vendor)
	}
	if got := strings.Join(taken.GetCredentialEnvVars(), ","); got != "FAKE_ACCESS_KEY,FAKE_PROFILE" {
		t.Errorf("WebIdentity.CredentialEnvVars = %q, want the variables that name the provider's own credential", got)
	}

	_, facts = configuredProvider(t, "1.2.3", fake.NewProvider(fake.Options{}))
	if facts.GetWebIdentity() != nil {
		t.Errorf("WebIdentity = %v for a provider that takes none, want none", facts.GetWebIdentity())
	}
}

package bootstrap

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/consent"
	"github.com/ocelhq/ocel/cli/internal/console"
	"github.com/ocelhq/ocel/cli/internal/version"
	"github.com/ocelhq/ocel/pkg/environment"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
)

func consoleTrustProject(t *testing.T, linked bool) (clitest.FakeProject, commands.Invocation, *clitest.FakeConsole) {
	t.Helper()
	project, invocation := bootstrapProject(t, "")
	project.Provider.FakeBootstrap().Offers(append(offered(), provider.Part{Name: provider.ConsoleTrustPart, Summary: "the console's trust"})...)
	project.Provider.WithFacts(func(facts *provider.Facts) {
		facts.WebIdentity = &provider.WebIdentityFacts{CredentialEnvVars: []string{"FAKE_ACCESS_KEY"}}
	})
	served := clitest.ServeConsole(t)
	invocation.Console = clitest.SignedInTo(served.URL)
	if linked {
		served.Link(t, project.Root)
	}
	return project, invocation, served
}

func trustedBefore(t *testing.T, project clitest.FakeProject) {
	t.Helper()
	trust := &provider.WebIdentityTrust{Issuer: "https://console.example.test/oidc", Audience: "https://console.example.test", OrganizationID: "org_1", TargetID: clitest.FixtureConsoleTargetID}
	if err := project.Provider.FakeBootstrap().Apply(context.Background(), provider.BootstrapRequest{Tier: environment.TierProduction, Parts: []string{provider.ConsoleTrustPart}, Trust: trust}, nil); err != nil {
		t.Fatal(err)
	}
}

func bootstrapWith(t *testing.T, project clitest.FakeProject, invocation commands.Invocation, opts Options) ([]*contractv1.BootstrapRequest, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(invocation, &stdout)
	err := Run(context.Background(), invocation, project.Root, environmentv1.Tier_TIER_PRODUCTION, opts, &stdout, &stderr, strings.NewReader(""))
	return clitest.RequestsTo[*contractv1.BootstrapRequest](t, project.Requests, contractv1connect.ProviderServiceBootstrapProcedure), stdout.String() + stderr.String(), err
}

func TestABootstrapLinkedToAConsoleTrustsItToDeployTheTierAndRecordsTheIdentity(t *testing.T) {
	project, invocation, served := consoleTrustProject(t, true)

	sent, out, err := bootstrapWith(t, project, invocation, Options{Yes: true})
	if err != nil {
		t.Fatalf("Run() = %v; %s", err, out)
	}

	prepared := served.PreparedTargets()
	if len(prepared) != 1 || prepared[0].GetVendor() != string(fake.Vendor) || prepared[0].GetAccount() != "000000000000" || prepared[0].GetProjectId() != clitest.FixtureConsoleProjectID {
		t.Errorf("Prepare got %v, want the provider's vendor and account and the linked project", prepared)
	}
	trust := sent[len(sent)-1].GetTrust()
	if trust.GetIssuer() != served.URL+"/oidc" || trust.GetAudience() != served.URL || trust.GetTargetId() != clitest.FixtureConsoleTargetID ||
		trust.GetOrganizationId() != "org_1" {
		t.Errorf("the bootstrap applied trust %v, want the prepared target's", trust)
	}
	recorded := served.RecordedTiers()
	if len(recorded) != 1 {
		t.Fatalf("RecordTier got %d requests, want 1", len(recorded))
	}
	if got := recorded[0]; got.GetTargetId() != clitest.FixtureConsoleTargetID || got.GetTier() != environmentv1.Tier_TIER_PRODUCTION ||
		got.GetIdentity().GetPrincipal() != "fake-deploy-production" || got.GetIdentity().GetIdentityProvider() != "fake-issuer" ||
		got.GetCliVersion() != version.Version || got.GetProjectId() != clitest.FixtureConsoleProjectID {
		t.Errorf("RecordTier got %v, want the identity the bootstrap made and the linked project", got)
	}
}

func TestAnUnlinkedCheckoutBootstrapsWithoutTheConsolesTrust(t *testing.T) {
	project, invocation, served := consoleTrustProject(t, false)

	sent, out, err := bootstrapWith(t, project, invocation, Options{Yes: true})
	if err != nil {
		t.Fatalf("Run() = %v; %s", err, out)
	}

	if len(served.PreparedTargets()) != 0 || sent[len(sent)-1].GetTrust() != nil {
		t.Errorf("an unlinked checkout prepared %v and sent trust %v, want neither", served.PreparedTargets(), sent[len(sent)-1].GetTrust())
	}
}

func TestADryBootstrapInventsNoTrustAndAsksTheConsoleNothing(t *testing.T) {
	project, invocation, served := consoleTrustProject(t, true)

	sent, out, err := bootstrapWith(t, project, invocation, Options{Dry: true})
	if err != nil {
		t.Fatalf("Run() = %v; %s", err, out)
	}

	if len(served.PreparedTargets()) != 0 || len(served.RecordedTiers()) != 0 || len(sent) == 0 || sent[0].GetTrust() != nil {
		t.Errorf("a dry run prepared %v, recorded %v and planned trust %v, want none", served.PreparedTargets(), served.RecordedTiers(), sent)
	}
	if !strings.Contains(out, "console-trust is added on the real run, once the console names the target") {
		t.Errorf("the dry run said %q, want it to say when console-trust is added", out)
	}
}

func TestADryBootstrapPlansFromTheRecordedTrust(t *testing.T) {
	project, invocation, served := consoleTrustProject(t, true)
	trustedBefore(t, project)

	sent, out, err := bootstrapWith(t, project, invocation, Options{Dry: true})
	if err != nil {
		t.Fatalf("Run() = %v; %s", err, out)
	}

	if len(served.PreparedTargets()) != 0 || sent[len(sent)-1].GetTrust().GetTargetId() != clitest.FixtureConsoleTargetID {
		t.Errorf("a dry run prepared %v and planned %v, want the recorded trust and no console call", served.PreparedTargets(), sent[len(sent)-1].GetTrust())
	}
}

func TestRemovingTheConsolesTrustTellsTheConsoleToForgetTheTierFirst(t *testing.T) {
	project, invocation, served := consoleTrustProject(t, true)
	trustedBefore(t, project)

	sent, out, err := bootstrapWith(t, project, invocation, Options{Yes: true, Remove: provider.ConsoleTrustPart})
	if err != nil {
		t.Fatalf("Run() = %v; %s", err, out)
	}

	forgotten := served.ForgottenTiers()
	if len(forgotten) != 1 || forgotten[0].GetTargetId() != clitest.FixtureConsoleTargetID || forgotten[0].GetTier() != environmentv1.Tier_TIER_PRODUCTION {
		t.Errorf("ForgetTier got %v, want the recorded target's production tier", forgotten)
	}
	if len(served.PreparedTargets()) != 0 || sent[len(sent)-1].GetTrust() != nil {
		t.Errorf("removing the trust prepared %v and sent %v, want only the part removed", served.PreparedTargets(), sent[len(sent)-1])
	}
}

func TestRemovingTheConsolesTrustWithNoConsoleToTellIsRefusedUnlessYes(t *testing.T) {
	project, invocation, _ := consoleTrustProject(t, true)
	invocation.Console.LoadCredentials = func() (console.Credentials, error) { return console.Credentials{}, console.ErrNotLoggedIn }
	trustedBefore(t, project)
	clitest.Bootstrap(t, project.Provider, environment.TierProduction, provider.ConsoleTrustPart)

	recorded := &contractv1.WebIdentityTrust{TargetId: clitest.FixtureConsoleTargetID}
	if err := forgetConsoleTrust(context.Background(), invocation, recorded, environmentv1.Tier_TIER_PRODUCTION, consent.Policy{Command: "ocel bootstrap production"}, nil); err == nil || !strings.Contains(err.Error(), "keeps issuing them") {
		t.Fatalf("forgetConsoleTrust() = %v unattended, want a refusal saying the console keeps issuing credentials", err)
	}
	if _, out, err := bootstrapWith(t, project, invocation, Options{Yes: true, Remove: provider.ConsoleTrustPart}); err != nil || !strings.Contains(out, "keeps issuing them") {
		t.Fatalf("Run(--yes) = %v; %s, want it removed with the warning said", err, out)
	}
}

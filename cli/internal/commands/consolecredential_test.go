package commands_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/console"
	"github.com/ocelhq/ocel/cli/internal/consolecredential"
	"github.com/ocelhq/ocel/cli/internal/githubactions/githubactionstest"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
)

const fakeCredentialEnvVar = "FAKE_ACCESS_KEY"

type consoleCredentialRun struct {
	project clitest.FakeProject
	console *clitest.FakeConsole
	env     map[string]string
}

func newConsoleCredentialRun(t *testing.T) consoleCredentialRun {
	t.Helper()
	project := clitest.SetUpProject(t)
	project.Provider.WithFacts(func(facts *provider.Facts) {
		facts.WebIdentity = &provider.WebIdentityFacts{CredentialEnvVars: []string{fakeCredentialEnvVar}}
	})
	served := clitest.ServeConsole(t)
	served.Credential = &consolev1.IssueCredentialResponse{
		Token:       "console-token",
		ExpiresAt:   timestamppb.New(time.Now().Add(time.Hour)),
		Account:     "000000000000",
		Region:      "nowhere",
		SessionName: "ocel-production-1",
		Identity:    &consolev1.TierIdentity{IdentityProvider: "console-issuer", Principal: "deploy-production"},
	}
	env := githubactionstest.Env(githubactionstest.ServeIDTokens(t, &githubactionstest.IDTokens{}))
	env[console.URLEnvVar] = served.URL
	env[console.ProjectIDEnvVar] = clitest.FixtureConsoleProjectID
	return consoleCredentialRun{project: project, console: served, env: env}
}

func (r consoleCredentialRun) open(t *testing.T) (*contractv1.ConfigureRequest, func(), error) {
	t.Helper()
	invocation := clitest.NewInvocation()
	invocation.ConsoleCredential = consolecredential.Console{Getenv: func(name string) string { return r.env[name] }, Version: "1.2.3"}
	cfg, err := invocation.LoadProject(context.Background(), r.project.Root)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	ctx, run, err := invocation.Events.Begin(context.Background(), "ocel test", cfg.Dir)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	var ended error
	t.Cleanup(func() { run.End(&ended) })
	opened, _, err := invocation.OpenProvider(ctx, run.Phase(progressv1.Phase_PHASE_CHECK), cfg, commands.OpenOptions{
		Intent: commands.UseInfrastructure, Tier: environmentv1.Tier_TIER_PRODUCTION, ConsoleCredential: true,
	})
	configured := clitest.RequestsTo[*contractv1.ConfigureRequest](t, r.project.Requests, "/provider.contract.v1.ProviderService/Configure")
	closeProvider := func() {}
	if opened != nil {
		closeProvider = opened.Close
	}
	if len(configured) == 0 {
		return nil, closeProvider, err
	}
	return configured[len(configured)-1], closeProvider, err
}

func TestAPipelineDeploysWithTheCredentialTheConsoleIssuesForItsTier(t *testing.T) {
	run := newConsoleCredentialRun(t)

	configured, closeProvider, err := run.open(t)
	if err != nil {
		t.Fatalf("OpenProvider: %v", err)
	}

	issued := run.console.IssuedCredentials()
	if len(issued) != 1 || issued[0].GetVendor() != string(fake.Vendor) || issued[0].GetTier() != environmentv1.Tier_TIER_PRODUCTION ||
		issued[0].GetProjectId() != clitest.FixtureConsoleProjectID {
		t.Errorf("IssueCredential got %v, want one request for the project's production tier", issued)
	}
	if configures := clitest.RequestsTo[*contractv1.ConfigureRequest](t, run.project.Requests, "/provider.contract.v1.ProviderService/Configure"); len(configures) != 1 {
		t.Errorf("the provider was configured %d times, want once", len(configures))
	}
	identity := configured.GetConfig().GetWebIdentity()
	if identity.GetPrincipal() != "deploy-production" || identity.GetIdentityProvider() != "console-issuer" || identity.GetSessionName() != "ocel-production-1" {
		t.Fatalf("the provider was configured with web identity %v, want the console's", identity)
	}
	token, err := os.ReadFile(identity.GetTokenFile())
	if err != nil || string(token) != "console-token" {
		t.Errorf("the token file holds %q, %v, want the console's token", token, err)
	}

	closeProvider()
	if _, err := os.Stat(identity.GetTokenFile()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the token file outlived the provider: %v", err)
	}
}

func TestAConsoleCredentialForAnotherAccountIsRefused(t *testing.T) {
	run := newConsoleCredentialRun(t)
	run.console.Credential.Account = "111111111111"

	_, closeProvider, err := run.open(t)
	defer closeProvider()

	if err == nil || !strings.Contains(err.Error(), "111111111111") || !strings.Contains(err.Error(), "000000000000") {
		t.Fatalf("OpenProvider err = %v, want the refusal naming both accounts", err)
	}
}

func TestAProjectTierTheConsoleBindsNoTargetToDeploysWithTheCredentialsAroundIt(t *testing.T) {
	run := newConsoleCredentialRun(t)
	run.console.CredentialRefusal = connect.NewError(connect.CodeNotFound, errors.New("no target"))

	configured, closeProvider, err := run.open(t)
	defer closeProvider()

	if err != nil {
		t.Fatalf("OpenProvider: %v", err)
	}
	if configured.GetConfig().GetWebIdentity() != nil {
		t.Errorf("the provider was configured with web identity %v, want the environment's credentials", configured.GetConfig().GetWebIdentity())
	}
}

func TestAConsoleRefusingTheCredentialStopsTheCommandWithItsReason(t *testing.T) {
	run := newConsoleCredentialRun(t)
	run.console.CredentialRefusal = connect.NewError(connect.CodeFailedPrecondition, errors.New("the production tier was bootstrapped without fake:deploy *"))

	_, closeProvider, err := run.open(t)
	defer closeProvider()

	if err == nil || !strings.Contains(err.Error(), "the production tier was bootstrapped without fake:deploy *") {
		t.Fatalf("OpenProvider err = %v, want the console's reason", err)
	}
}

func TestARunNamingItsOwnCredentialAndAConsoleProjectIsRefusedRatherThanGuessed(t *testing.T) {
	run := newConsoleCredentialRun(t)
	t.Setenv(fakeCredentialEnvVar, "AKIAEXAMPLE")

	_, closeProvider, err := run.open(t)
	defer closeProvider()

	if err == nil || !strings.Contains(err.Error(), fakeCredentialEnvVar) {
		t.Fatalf("OpenProvider err = %v, want a refusal naming %s", err, fakeCredentialEnvVar)
	}
}

package build

import (
	"bytes"
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/jobcontext"
	"github.com/ocelhq/ocel/cli/internal/lockfile"
	"github.com/ocelhq/ocel/cli/internal/project"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	runnerv1 "github.com/ocelhq/ocel/pkg/proto/runner/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
)

func TestBuildOfAProjectNamingNoProviderRunsAsAnOcelCloudBuildJobWhenLoggedIn(t *testing.T) {
	fake := clitest.ServeConsole(t)
	dependencies := newTestDependencies()
	fake.LogIn(&dependencies.Invocation)
	dependencies.BuildApps = func(context.Context, *project.Project, map[string]build.AppVariables, map[string]string, build.HostedWorkers, build.Host, build.Log) (build.Output, error) {
		t.Fatal("built locally for an Ocel Cloud target")
		return build.Output{}, nil
	}
	root := t.TempDir()
	clitest.WriteFile(t, filepath.Join(root, "ocel.json"), `{"slug": "shop", "apps": [{"name": "api", "path": ".", "framework": "node", "compute": "serverless"}]}`)
	clitest.WriteFile(t, filepath.Join(root, "index.ts"), "export {}\n")

	var out bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &out)
	if err := runBuild(t.Context(), dependencies, root, options{}, strings.NewReader("")); err != nil {
		t.Fatal(err)
	}
	if len(fake.Dispatched) != 1 || fake.Dispatched[0].GetJob().GetCommand() != runnerv1.JobCommand_JOB_COMMAND_BUILD || fake.Dispatched[0].GetJob().GetSourceId() != clitest.FakeCloudSourceID ||
		fake.Dispatched[0].GetTier() != environmentv1.Tier_TIER_PRODUCTION || fake.Dispatched[0].GetJob().GetTierValues() {
		t.Fatalf("dispatched %v, want one build job of the committed source, for production's runner", fake.Dispatched)
	}
}

func TestBuildRefusesNoWaitWhenTheProjectDoesNotRunOnOcelCloud(t *testing.T) {
	dependencies := newTestDependencies()
	root := t.TempDir()
	clitest.WriteFile(t, filepath.Join(root, "ocel.json"), `{"slug": "shop", "apps": [{"name": "api", "path": ".", "framework": "node", "compute": "serverless"}]}`)
	clitest.WriteFile(t, filepath.Join(root, "index.ts"), "export {}\n")

	err := runBuild(t.Context(), dependencies, root, options{noWait: true}, strings.NewReader(""))
	if err == nil || !strings.Contains(err.Error(), "--no-wait") {
		t.Fatalf("err = %v, want --no-wait refused", err)
	}
}

func TestBuildInsideAJobOfAProjectNamingNoProviderOpensOcelCloudWithItsJobTokenAndWritesNoLock(t *testing.T) {
	ocelCloud := fake.NewForProject(fake.Options{})
	ocelCloud.WithFacts(func(facts *provider.Facts) {
		facts.WebIdentity = &provider.WebIdentityFacts{CredentialEnvVars: []string{"FAKE_ACCESS_KEY"}}
	})
	requests := clitest.ServeFake(t, ocelCloud)
	testBinary, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	clitest.InstallProvider(t, commands.CloudProviderID, func(dest string) error { return os.Symlink(testBinary, dest) })
	cloud := clitest.ServeConsole(t)
	tokenFile := filepath.Join(t.TempDir(), "token")
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"org:org_1:tier:production:project:p"}`))
	if err := os.WriteFile(tokenFile, []byte("e30."+payload+".c2ln"), 0o600); err != nil {
		t.Fatal(err)
	}
	dependencies := newTestDependencies()
	dependencies.JobContext = &jobcontext.Context{
		ConsoleURL: cloud.URL, ProjectID: clitest.FixtureConsoleProjectID, Credential: jobcontext.JobToken{File: tokenFile},
		Job: &jobcontext.Job{ID: "job_1", Slug: "shop", Tier: environmentv1.Tier_TIER_PRODUCTION},
	}
	var built *project.Project
	dependencies.BuildApps = func(_ context.Context, cfg *project.Project, _ map[string]build.AppVariables, _ map[string]string, _ build.HostedWorkers, _ build.Host, _ build.Log) (build.Output, error) {
		built = cfg
		return build.Output{Functions: []build.Function{{Route: "index", App: "api"}}}, nil
	}
	root := t.TempDir()
	clitest.WriteFile(t, filepath.Join(root, "ocel.json"), `{"slug": "shop", "apps": [{"name": "api", "path": ".", "framework": "node"}]}`)
	clitest.WriteFile(t, filepath.Join(root, "index.js"), "export {};\n")

	var out bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &out)
	if err := runBuild(t.Context(), dependencies, root, options{}, strings.NewReader("")); err != nil {
		t.Fatalf("runBuild: %v\n%s", err, out.String())
	}
	if built == nil || built.Apps[0].Compute == "" {
		t.Fatalf("built %+v, want the app's compute resolved by the provider %s", built, commands.CloudProviderID)
	}
	configured := clitest.RequestsTo[*contractv1.ConfigureRequest](t, requests, "/provider.contract.v1.ProviderService/Configure")
	identity := configured[len(configured)-1].GetConfig().GetWebIdentity()
	if identity.GetTokenFile() != tokenFile || identity.GetIdentityProvider() != cloud.Runner.URL || identity.GetPrincipal() != "org:org_1:tier:production:project:p" {
		t.Fatalf("the provider was configured with %v, want the job token as its web identity at Ocel Cloud", identity)
	}
	if _, err := os.Stat(filepath.Join(root, lockfile.Name)); !os.IsNotExist(err) {
		t.Fatalf("stat %s: %v, want no lock written inside a job", lockfile.Name, err)
	}
}

func TestBuildWithAnEnvRunsOnThatTiersRunner(t *testing.T) {
	cloud := clitest.ServeConsole(t)
	dependencies := newTestDependencies()
	dependencies.ReadGitBranch = func(context.Context, string) (string, error) { return "feature/login", nil }
	dependencies.DiscoverPRNumber = func() string { return "" }
	cloud.LogIn(&dependencies.Invocation)
	root := t.TempDir()
	clitest.WriteFile(t, filepath.Join(root, "ocel.json"), `{"slug": "shop", "apps": [{"name": "api", "path": ".", "framework": "node", "compute": "serverless"}]}`)
	clitest.WriteFile(t, filepath.Join(root, "index.ts"), "export {}\n")

	if err := runBuild(t.Context(), dependencies, root, options{env: "preview"}, strings.NewReader("")); err != nil {
		t.Fatal(err)
	}
	if len(cloud.Resolved) != 1 || cloud.Resolved[0].GetTier() != environmentv1.Tier_TIER_PREVIEW || len(cloud.Dispatched) != 1 || cloud.Dispatched[0].GetTier() != environmentv1.Tier_TIER_PREVIEW ||
		!cloud.Dispatched[0].GetJob().GetTierValues() {
		t.Errorf("resolved %v and dispatched %v, want the build job on preview's runner, reading preview's values", cloud.Resolved, cloud.Dispatched)
	}
}

package deploy

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/commands/bootstrap"
	"github.com/ocelhq/ocel/cli/internal/commands/domain"
	"github.com/ocelhq/ocel/cli/internal/commands/projectinit"
	"github.com/ocelhq/ocel/cli/internal/prerequisite"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/configdoc"
	"github.com/ocelhq/ocel/pkg/environment"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
)

type answer struct {
	line   string
	before func()
}

type scriptedAnswers struct {
	steps   []answer
	pending []byte
}

func answering(steps ...answer) *scriptedAnswers { return &scriptedAnswers{steps: steps} }

func (a *scriptedAnswers) Read(p []byte) (int, error) {
	if len(a.pending) == 0 {
		if len(a.steps) == 0 {
			return 0, io.EOF
		}
		step := a.steps[0]
		a.steps = a.steps[1:]
		if step.before != nil {
			step.before()
		}
		a.pending = []byte(step.line + "\n")
	}
	n := copy(p, a.pending)
	a.pending = a.pending[n:]
	return n, nil
}

func withSetups(dependencies *Dependencies) {
	initDependencies := projectinit.Dependencies{
		Invocation:        dependencies.Invocation,
		RunPackageManager: func(context.Context, string, []string, io.Writer) error { return nil },
	}
	dependencies.Setups = prerequisite.Setups{
		prerequisite.Project:   projectinit.NewSetup(initDependencies),
		prerequisite.Bootstrap: bootstrap.NewSetup(),
		prerequisite.Domain:    domain.NewSetup(),
	}
}

func emptyProjectDir(t *testing.T) (string, clitest.FakeProject) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("uses a Unix-domain-socket fake provider")
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not found on PATH")
	}
	t.Setenv(providerprocess.ReadyTimeoutEnvVar, "5s")
	root := filepath.Join(t.TempDir(), "shop")
	clitest.WriteFile(t, filepath.Join(root, "package.json"), "{}\n")
	p := fake.NewForProject(fake.Options{}, root)
	return root, clitest.FakeProject{Root: root, Provider: p, Requests: clitest.ServeFake(t, p)}
}

func fakeChoice(t *testing.T) string {
	t.Helper()
	return strconv.Itoa(slices.Index(configdoc.ProviderIDs(), string(fake.Vendor)) + 1)
}

func saveHostname(t *testing.T, root string) func() {
	return func() {
		clitest.WriteFile(t, filepath.Join(root, project.DefaultFileName),
			`{"slug": "shop", "provider": {"fake": {}}, "domains": {"production": "shop.example.com"}}`)
	}
}

func rootFunction(app string) []build.Function {
	return []build.Function{{Route: "index", Framework: buildoutput.Framework{Name: "node"}, EntryFile: "src/server.js", ArtifactPath: "output", App: app}}
}

func TestADeployInAnEmptyDirectoryInitializesBootstrapsAndDeploys(t *testing.T) {
	root, fixture := emptyProjectDir(t)
	dependencies := newTestDependencies()
	terminalStdin(&dependencies)
	withSetups(&dependencies)
	stubBuild(&dependencies, rootFunction("shop"))

	stdin := answering(
		answer{line: "y"},
		answer{line: fakeChoice(t)},
		answer{line: "y"},
		answer{line: "y"},
		answer{line: "", before: saveHostname(t, root)},
	)
	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runDeploy(context.Background(), dependencies, root, deployOptions{}, &stdout, &stderr, stdin); err != nil {
		t.Fatalf("runDeploy err = %v; stdout=%s", err, stdout.String())
	}

	if _, err := os.Stat(filepath.Join(root, project.DefaultFileName)); err != nil {
		t.Errorf("no config after the deploy set the project up: %v", err)
	}
	var applied []string
	for _, procedure := range fixture.Requests.Procedures() {
		switch procedure {
		case contractv1connect.ProviderServiceBootstrapProcedure, contractv1connect.ProviderServiceDeployProcedure:
			applied = append(applied, procedure)
		}
	}
	if want := []string{contractv1connect.ProviderServiceBootstrapProcedure, contractv1connect.ProviderServiceBootstrapProcedure, contractv1connect.ProviderServiceDeployProcedure}; !slices.Equal(applied, want) {
		t.Errorf("the provider received %q, want the bootstrap planned, then applied, then one deploy", applied)
	}
	if !strings.Contains(stdout.String(), "Deployed shop to production") {
		t.Errorf("stdout = %q, want the deploy to finish", stdout.String())
	}
}

func TestADeployWithNoInfrastructureAndNoTerminalNamesTheBootstrapToRun(t *testing.T) {
	fixture := setUpDeployProject(t)
	removeBootstrap(t, fixture, environment.TierProduction)
	dependencies := newTestDependencies()
	withSetups(&dependencies)
	stubBuild(&dependencies, nil)

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	err := runDeploy(context.Background(), dependencies, fixture.Root, deployOptions{}, &stdout, &stderr, strings.NewReader("y\n"))
	if err == nil {
		t.Fatalf("runDeploy err = nil, want the missing bootstrap refused; stdout=%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "Run `ocel bootstrap production") {
		t.Errorf("stdout = %q, want the refusal to name `ocel bootstrap production`", stdout.String())
	}
	if bootstraps := clitest.RequestsTo[*contractv1.BootstrapRequest](t, fixture.Requests, contractv1connect.ProviderServiceBootstrapProcedure); len(bootstraps) != 0 {
		t.Errorf("the provider was bootstrapped %d times without a terminal, want never", len(bootstraps))
	}
}

func TestADeployWithNoHostnameInATerminalWaitsForTheEditThenDeploys(t *testing.T) {
	fixture := setUpDeployProject(t)
	clitest.WriteFile(t, filepath.Join(fixture.Root, "ocel.config.ts"), `
export default {
  slug: "`+clitest.FixtureSlug+`",
  provider: { fake: {} },
};
`)
	dependencies := newTestDependencies()
	terminalStdin(&dependencies)
	withSetups(&dependencies)
	stubBuild(&dependencies, nil)

	stdin := answering(answer{line: "", before: func() { writeConfig(t, fixture.Root, "") }})
	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runDeploy(context.Background(), dependencies, fixture.Root, deployOptions{}, &stdout, &stderr, stdin); err != nil {
		t.Fatalf("runDeploy err = %v; stdout=%s", err, stdout.String())
	}
	if !strings.Contains(stdout.String(), `domains: { production: "`+clitest.FixtureSlug+`.example.com" },`) {
		t.Errorf("stdout = %q, want the snippet in the TypeScript config's own form", stdout.String())
	}
	sent := sentDeploys(t, fixture)
	if len(sent) != 1 {
		t.Fatalf("the CLI sent %d deploys, want 1 once the hostname was saved", len(sent))
	}
	if got := sentPreflights(t, fixture); len(got) == 0 || !slices.Contains(got[len(got)-1].GetDomains(), productionDomain) {
		t.Errorf("the last of %d preflights named %v, want the hostname the reloaded config declares", len(got), got[len(got)-1].GetDomains())
	}
}

func TestADeployUnderJSONOnATerminalOffersNoSetupAndFailsWithTheMissingPrerequisite(t *testing.T) {
	for _, tc := range []struct {
		name    string
		prepare func(t *testing.T, fixture clitest.FakeProject)
		code    string
		hint    string
	}{
		{"no infrastructure", func(t *testing.T, fixture clitest.FakeProject) {
			removeBootstrap(t, fixture, environment.TierProduction)
		}, "bootstrap.missing", "ocel bootstrap production --features cache,images"},
		{"no production hostname", func(t *testing.T, fixture clitest.FakeProject) {
			clitest.WriteFile(t, filepath.Join(fixture.Root, "ocel.config.ts"), "export default {\n  slug: \""+clitest.FixtureSlug+"\",\n  provider: { fake: {} },\n};\n")
		}, "prerequisite.missing", "domains.production"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := setUpDeployProject(t)
			tc.prepare(t, fixture)
			dependencies := newTestDependencies()
			withSetups(&dependencies)
			stubBuild(&dependencies, nil)
			tty, screen := clitest.UnderJSONOnATerminal(t, &dependencies.Invocation)
			var stdout bytes.Buffer
			dependencies.Events.Attach(terminal.NewJSONLines(&stdout))

			err := clitest.FinishWithin(t, 20*time.Second, func(ctx context.Context) error {
				return runDeploy(ctx, dependencies, fixture.Root, deployOptions{}, &stdout, &stdout, tty)
			})

			if err == nil {
				t.Error("runDeploy err = nil, want the missing prerequisite refused")
			}
			if asked := screen(); asked != "" {
				t.Errorf("terminal = %q, want nothing asked under --json", asked)
			}
			evs := clitest.RunEvents(t, stdout.String())
			if got := evs[len(evs)-1].GetSummary().GetError(); got.GetCode() != tc.code || got.GetHint() != tc.hint {
				t.Errorf("summary error = %v, want %s hinting %s", got, tc.code, tc.hint)
			}
			if bootstraps := clitest.RequestsTo[*contractv1.BootstrapRequest](t, fixture.Requests, contractv1connect.ProviderServiceBootstrapProcedure); len(bootstraps) != 0 {
				t.Errorf("the provider was sent %d bootstraps under --json, want none", len(bootstraps))
			}
			if sent := sentDeploys(t, fixture); len(sent) != 0 {
				t.Errorf("the CLI sent %d deploys, want none", len(sent))
			}
		})
	}
}

func TestADeclinedSetupExitsZeroNamingTheCommand(t *testing.T) {
	fixture := setUpDeployProject(t)
	removeBootstrap(t, fixture, environment.TierProduction)
	dependencies := newTestDependencies()
	terminalStdin(&dependencies)
	withSetups(&dependencies)
	stubBuild(&dependencies, nil)

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runDeploy(context.Background(), dependencies, fixture.Root, deployOptions{}, &stdout, &stderr, strings.NewReader("n\n")); err != nil {
		t.Fatalf("runDeploy err = %v, want a declined setup to exit 0; stdout=%s", err, stdout.String())
	}
	if want := "Not set up, so this run changes nothing. When you're ready: `ocel bootstrap production"; !strings.Contains(stdout.String(), want) {
		t.Errorf("stdout = %q, want %q", stdout.String(), want)
	}
	if sent := sentDeploys(t, fixture); len(sent) != 0 {
		t.Errorf("the CLI sent %d deploys after the setup was declined, want none", len(sent))
	}
}

func TestAcceptingTheBootstrapOfferShowsItsPlanAndAsksBeforeApplyingIt(t *testing.T) {
	fixture := setUpDeployProject(t)
	removeBootstrap(t, fixture, environment.TierProduction)
	dependencies := newTestDependencies()
	terminalStdin(&dependencies)
	withSetups(&dependencies)
	stubBuild(&dependencies, nil)
	before := len(fixture.Provider.FakeBootstrap().Applied())

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	var shownBeforeAsked bool
	stdin := answering(
		answer{line: "y"},
		answer{line: "y", before: func() {
			shownBeforeAsked = strings.Contains(stdout.String(), "Proposed changes to the production bootstrap") &&
				len(fixture.Provider.FakeBootstrap().Applied()) == before
		}},
	)
	if err := runDeploy(context.Background(), dependencies, fixture.Root, deployOptions{}, &stdout, &stderr, stdin); err != nil {
		t.Fatalf("runDeploy err = %v; stdout=%s", err, stdout.String())
	}
	if !shownBeforeAsked {
		t.Errorf("stdout = %q, want the bootstrap plan shown, and nothing applied, before its confirmation is answered", stdout.String())
	}
	var applying []*contractv1.BootstrapRequest
	for _, req := range clitest.RequestsTo[*contractv1.BootstrapRequest](t, fixture.Requests, contractv1connect.ProviderServiceBootstrapProcedure) {
		if !req.GetDry() {
			applying = append(applying, req)
		}
	}
	if len(applying) != 1 || applying[0].GetConsented() == nil {
		t.Errorf("the provider was sent %v to apply, want one bootstrap carrying the plan that was consented to", applying)
	}
	if sent := sentDeploys(t, fixture); len(sent) != 1 {
		t.Errorf("the CLI sent %d deploys, want 1 once the bootstrap was applied", len(sent))
	}
}

func TestDecliningTheBootstrapPlanAppliesNothingAndExitsZero(t *testing.T) {
	fixture := setUpDeployProject(t)
	removeBootstrap(t, fixture, environment.TierProduction)
	dependencies := newTestDependencies()
	terminalStdin(&dependencies)
	withSetups(&dependencies)
	stubBuild(&dependencies, nil)
	before := len(fixture.Provider.FakeBootstrap().Applied())

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runDeploy(context.Background(), dependencies, fixture.Root, deployOptions{}, &stdout, &stderr, strings.NewReader("y\nn\n")); err != nil {
		t.Fatalf("runDeploy err = %v, want a declined plan to exit 0; stdout=%s", err, stdout.String())
	}
	if applied := fixture.Provider.FakeBootstrap().Applied()[before:]; len(applied) != 0 {
		t.Errorf("the provider applied %+v after its plan was declined, want nothing", applied)
	}
	if !strings.Contains(stdout.String(), "Not confirmed, so this run changes nothing") {
		t.Errorf("stdout = %q, want the declined plan to say the run changes nothing", stdout.String())
	}
	if sent := sentDeploys(t, fixture); len(sent) != 0 {
		t.Errorf("the CLI sent %d deploys after the plan was declined, want none", len(sent))
	}
}

func TestAFirstDeployOfAProjectNeedingAFeatureBootstrapsWithItAndDeploysInOneRun(t *testing.T) {
	fixture := clitest.SetUpProject(t)
	removeBootstrap(t, fixture, environment.TierProduction)
	fixture.Provider.FakeBootstrap().Offers(provider.Feature{Name: fake.FeatureCache, Summary: "a cache every node app needs", Frameworks: []string{"node"}})
	writeUsageMonorepo(t, fixture.Root, "  edge: \"direct\",\n")
	before := len(fixture.Provider.FakeBootstrap().Applied())
	dependencies := newTestDependencies()
	terminalStdin(&dependencies)
	withSetups(&dependencies)
	stubBuild(&dependencies, apiFunction())

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runDeploy(context.Background(), dependencies, fixture.Root, deployOptions{}, &stdout, &stderr, strings.NewReader("y\ny\n")); err != nil {
		t.Fatalf("runDeploy err = %v; stdout=%s", err, stdout.String())
	}
	applied := fixture.Provider.FakeBootstrap().Applied()[before:]
	if len(applied) != 1 || !slices.Contains(applied[0].Features, fake.FeatureCache) {
		t.Errorf("the provider applied %+v, want one bootstrap that includes %s, which the project needs", applied, fake.FeatureCache)
	}
	if sent := sentDeploys(t, fixture); len(sent) != 1 {
		t.Errorf("the CLI sent %d deploys, want the first bootstrap to leave nothing missing for the deploy; stdout=%s", len(sent), stdout.String())
	}
}

func TestADeployAgainstABootstrapLackingAFeatureItNeedsAddsItAndDeploysInOneRun(t *testing.T) {
	fixture := clitest.SetUpProject(t)
	clitest.Bootstrap(t, fixture.Provider, environment.TierProduction)
	fixture.Provider.FakeBootstrap().Offers(provider.Feature{Name: fake.FeatureCache, Summary: "a cache every node app needs", Frameworks: []string{"node"}})
	writeUsageMonorepo(t, fixture.Root, "  edge: \"direct\",\n")
	before := len(fixture.Provider.FakeBootstrap().Applied())
	dependencies := newTestDependencies()
	terminalStdin(&dependencies)
	withSetups(&dependencies)
	stubBuild(&dependencies, apiFunction())

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runDeploy(context.Background(), dependencies, fixture.Root, deployOptions{}, &stdout, &stderr, strings.NewReader("y\ny\n")); err != nil {
		t.Fatalf("runDeploy err = %v, want the missing feature offered before the build; stdout=%s", err, stdout.String())
	}
	if !strings.Contains(stdout.String(), "does not include what this project needs: "+fake.FeatureCache) {
		t.Errorf("stdout = %q, want the check to name the feature the bootstrap lacks", stdout.String())
	}
	applied := fixture.Provider.FakeBootstrap().Applied()[before:]
	if len(applied) != 1 || !slices.Contains(applied[0].Features, fake.FeatureCache) {
		t.Errorf("the provider applied %+v, want one bootstrap that adds %s", applied, fake.FeatureCache)
	}
	if sent := sentDeploys(t, fixture); len(sent) != 1 {
		t.Errorf("the CLI sent %d deploys, want one once the feature was added; stdout=%s", len(sent), stdout.String())
	}
}

//go:build unix

package deploy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/localrpc"
	"github.com/ocelhq/ocel/pkg/processenv"
	"github.com/ocelhq/ocel/pkg/progress"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

type preBuildProject struct {
	fixture      clitest.FakeProject
	dependencies Dependencies
	seen         *forwardsSeen
	marker       string
	built        *bool
	openAtBuild  *int
}

func setUpPreBuildProject(t *testing.T, preBuild string) preBuildProject {
	t.Helper()
	dependencies := newTestDependencies()
	stubBuild(&dependencies, apiFunction())
	fixture := setUpDeployProject(t)
	writeUsageMonorepo(t, fixture.Root, "")
	writeConfigWithLifecycle(t, fixture.Root, preBuild)
	seen := forwardingPorts(t, fixture)

	built, openAtBuild := false, -1
	inner := dependencies.BuildApps
	dependencies.BuildApps = func(ctx context.Context, cfg *project.Project, variables map[string]build.AppVariables, archs map[string]string, workers build.HostedWorkers, host build.Host, log build.Log) (build.Output, error) {
		built = true
		seen.mutex.Lock()
		openAtBuild = seen.open
		seen.mutex.Unlock()
		return inner(ctx, cfg, variables, archs, workers, host, log)
	}
	return preBuildProject{fixture: fixture, dependencies: dependencies, seen: seen, marker: filepath.Join(t.TempDir(), "marker"), built: &built, openAtBuild: &openAtBuild}
}

func (p preBuildProject) deploy(t *testing.T, opts deployOptions) (string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(p.dependencies.Invocation, &stdout)
	err := runDeploy(context.Background(), p.dependencies, p.fixture.Root, opts, &stdout, &stderr, strings.NewReader(""))
	return stdout.String() + stderr.String(), err
}

func (p preBuildProject) ran(t *testing.T) bool {
	t.Helper()
	_, err := os.Stat(p.marker)
	return err == nil
}

func (p preBuildProject) procedures() []string {
	return p.fixture.Requests.Procedures()
}

func TestAPreBuildRunsOnceAfterInfraIsProvisionedAndPortsAreForwardedAndBeforeTheBuild(t *testing.T) {
	p := setUpPreBuildProject(t, `"true"`)
	live := filepath.Join(t.TempDir(), "binding.json")
	command := fmt.Sprintf(`cat "$OCEL_LIVE_DIR/OCEL_RESOURCE_POSTGRES_main" > %s && echo ran >> %s`, live, p.marker)
	writeConfigWithLifecycle(t, p.fixture.Root, fmt.Sprintf("%q", command))

	out, err := p.deploy(t, deployOptions{yes: true})
	if err != nil {
		t.Fatalf("runDeploy err = %v; out=%s", err, out)
	}

	if got := strings.Count(readOrEmpty(t, p.marker), "ran"); got != 1 {
		t.Errorf("the preBuild ran %d times, want once per deploy", got)
	}
	var delivered bindingsv1.Binding
	if err := protojson.Unmarshal([]byte(readOrEmpty(t, live)), &delivered); err != nil || delivered.GetPostgres().GetPort() != 41234 {
		t.Errorf("the preBuild read %q as main's binding (%v), want the one forwarded to 127.0.0.1:41234", readOrEmpty(t, live), err)
	}
	if !*p.built || *p.openAtBuild != 1 {
		t.Errorf("the build ran = %v with %d forwards open, want it after the preBuild with the forwards still open", *p.built, *p.openAtBuild)
	}
	procedures := p.procedures()
	provisioned := slices.Index(procedures, contractv1connect.ProviderServiceProvisionInfraProcedure)
	forwarded := slices.Index(procedures, contractv1connect.ProviderServiceForwardPortsProcedure)
	deployedAt := slices.Index(procedures, contractv1connect.ProviderServiceDeployProcedure)
	if provisioned < 0 || forwarded < provisioned || deployedAt < forwarded {
		t.Errorf("the provider was called %v, want ProvisionInfra, then ForwardPorts, then Deploy", procedures)
	}
	p.seen.mutex.Lock()
	defer p.seen.mutex.Unlock()
	if !slices.Equal(p.seen.openAtDeploy, []int{0}) {
		t.Errorf("the deploy began with %v forwards open, want them closed first", p.seen.openAtDeploy)
	}
}

func TestAPreBuildIsHandedTheBindingsOfEveryResourceTheProjectProvisionsWhetherOrNotAnAppBuildsWithThem(t *testing.T) {
	p := setUpPreBuildProject(t, `"true"`)

	if out, err := p.deploy(t, deployOptions{yes: true}); err != nil {
		t.Fatalf("runDeploy err = %v; out=%s", err, out)
	}

	sent := clitest.RequestsTo[*contractv1.ForwardPortsRequest](t, p.fixture.Requests, contractv1connect.ProviderServiceForwardPortsProcedure)
	if len(sent) != 1 || !slices.Equal(sent[0].GetBindings(), []string{"db--main"}) {
		t.Errorf("the CLI asked to forward %v, want db--main though no app of this project builds with bindings", sent)
	}
}

func TestAPreBuildsOutputStreamsIntoTheDeployAndHidesWhatTheBindingsKeepSecret(t *testing.T) {
	p := setUpPreBuildProject(t, `"true"`)
	writeConfigWithLifecycle(t, p.fixture.Root, `"echo migrating; cat \"$OCEL_LIVE_DIR/OCEL_RESOURCE_POSTGRES_main\""`)

	out, err := p.deploy(t, deployOptions{yes: true})
	if err != nil {
		t.Fatalf("runDeploy err = %v; out=%s", err, out)
	}

	if !strings.Contains(out, "migrating") {
		t.Errorf("the deploy said %q, want the preBuild's output in it", out)
	}
	if !strings.Contains(out, "lifecycle.preBuild") {
		t.Errorf("the deploy said %q, want the preBuild named as the config names it", out)
	}
	live := p.liveBinding(t)
	if password := live.GetPostgres().GetPassword(); password == "" || strings.Contains(out, password) {
		t.Errorf("the password %q appears in the deploy's output, want it hidden", password)
	}
}

func (p preBuildProject) liveBinding(t *testing.T) *bindingsv1.Binding {
	t.Helper()
	writeConfigWithLifecycle(t, p.fixture.Root, fmt.Sprintf(`"cat \"$OCEL_LIVE_DIR/OCEL_RESOURCE_POSTGRES_main\" > %s"`, p.marker))
	if out, err := p.deploy(t, deployOptions{yes: true}); err != nil {
		t.Fatalf("runDeploy err = %v; out=%s", err, out)
	}
	var live bindingsv1.Binding
	if err := protojson.Unmarshal([]byte(readOrEmpty(t, p.marker)), &live); err != nil {
		t.Fatalf("read the live binding: %v", err)
	}
	return &live
}

func TestAPreBuildThatExitsNonZeroStopsTheDeployBeforeItBuildsAndLeavesNothingPromoted(t *testing.T) {
	p := setUpPreBuildProject(t, `"echo schema broken >&2; exit 4"`)

	out, err := p.deploy(t, deployOptions{yes: true})

	if err == nil || !strings.Contains(err.Error(), "exited with code 4") {
		t.Fatalf("runDeploy err = %v, want the preBuild's exit code", err)
	}
	if !strings.Contains(out, "schema broken") {
		t.Errorf("the deploy said %q, want the failing command's output in it", out)
	}
	if *p.built {
		t.Error("the apps were built after the preBuild failed")
	}
	procedures := p.procedures()
	if !slices.Contains(procedures, contractv1connect.ProviderServiceProvisionInfraProcedure) {
		t.Errorf("the provider was called %v, want the infrastructure left converged", procedures)
	}
	if slices.Contains(procedures, contractv1connect.ProviderServiceDeployProcedure) {
		t.Errorf("the provider was called %v, want nothing promoted", procedures)
	}
	deadline := time.Now().Add(2 * time.Second)
	for p.seen.openNow() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if open := p.seen.openNow(); open != 0 {
		t.Errorf("%d forwards are still open after the deploy failed", open)
	}
}

func TestAPreBuildThatRunsPastItsTimeoutStopsTheDeployBeforeItBuilds(t *testing.T) {
	p := setUpPreBuildProject(t, `{ command: "sleep 30", timeout: "300ms" }`)

	_, err := p.deploy(t, deployOptions{yes: true})

	if err == nil || !strings.Contains(err.Error(), "timeout of 300ms") {
		t.Fatalf("runDeploy err = %v, want the timeout named", err)
	}
	if *p.built {
		t.Error("the apps were built after the preBuild timed out")
	}
}

func TestAPreBuildRunsFromTheDirectoryOfTheAppItNames(t *testing.T) {
	p := setUpPreBuildProject(t, `"true"`)
	writeConfigWithLifecycle(t, p.fixture.Root, fmt.Sprintf(`{ command: "pwd -P > %s", app: "api" }`, p.marker))

	if out, err := p.deploy(t, deployOptions{yes: true}); err != nil {
		t.Fatalf("runDeploy err = %v; out=%s", err, out)
	}

	want, err := filepath.EvalSymlinks(filepath.Join(p.fixture.Root, "apps", "api"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(readOrEmpty(t, p.marker)); got != want {
		t.Errorf("the preBuild ran in %q, want the app's directory %q", got, want)
	}
}

func TestAPreBuildWithoutAnAppRunsFromTheProjectDirectory(t *testing.T) {
	p := setUpPreBuildProject(t, `"true"`)
	writeConfigWithLifecycle(t, p.fixture.Root, fmt.Sprintf(`"pwd -P > %s"`, p.marker))

	if out, err := p.deploy(t, deployOptions{yes: true}); err != nil {
		t.Fatalf("runDeploy err = %v; out=%s", err, out)
	}

	want, err := filepath.EvalSymlinks(p.fixture.Root)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(readOrEmpty(t, p.marker)); got != want {
		t.Errorf("the preBuild ran in %q, want the project directory %q", got, want)
	}
}

func TestAPreBuildOfAProviderThatForwardsNoPortRunsWithoutBindingsAndSaysSo(t *testing.T) {
	p := setUpPreBuildProject(t, `"true"`)
	writeConfigWithLifecycle(t, p.fixture.Root, fmt.Sprintf(`"echo ran > %s; test -z \"$OCEL_LIVE_DIR\""`, p.marker))
	p.fixture.Provider.WithHooks(func(h *provider.Hooks) { h.ForwardPorts = nil })

	out, err := p.deploy(t, deployOptions{yes: true})

	if err != nil {
		t.Fatalf("runDeploy err = %v; out=%s", err, out)
	}
	if !p.ran(t) {
		t.Error("the preBuild did not run")
	}
	if !strings.Contains(out, "forwards no port") {
		t.Errorf("the deploy said %q, want it to say the command goes without bindings", out)
	}
}

func TestAPreBuildWhoseForwardIsRefusedStopsTheDeployBeforeItRunsAndNamesTheResource(t *testing.T) {
	p := setUpPreBuildProject(t, `"true"`)
	writeConfigWithLifecycle(t, p.fixture.Root, fmt.Sprintf(`"touch %s"`, p.marker))
	p.fixture.Provider.WithHooks(func(h *provider.Hooks) {
		h.ForwardPorts = func(context.Context, provider.PortForwardRequest, progress.Log) ([]provider.PortForward, error) {
			return nil, refusal.Refuse(refusal.CodeNotReady, "container db holds no address on any network, so it is not running")
		}
	})

	out, err := p.deploy(t, deployOptions{yes: true})

	if err == nil || !strings.Contains(err.Error(), "lifecycle.preBuild") || !strings.Contains(err.Error(), "main") || !strings.Contains(err.Error(), "is not running") {
		t.Fatalf("runDeploy err = %v, want the preBuild refused, naming main and why; out=%s", err, out)
	}
	if p.ran(t) {
		t.Error("the preBuild ran without the bindings it was refused")
	}
	if *p.built {
		t.Error("the apps were built after the preBuild was refused its bindings")
	}
	if slices.Contains(p.procedures(), contractv1connect.ProviderServiceDeployProcedure) {
		t.Errorf("the provider was called %v, want nothing promoted", p.procedures())
	}
}

func TestAPreBuildAsksForTheForwardsOfTheDeploysTierAndAWayToReportOneThatFails(t *testing.T) {
	p := setUpPreBuildProject(t, `"true"`)

	out, err := p.deploy(t, deployOptions{yes: true})
	if err != nil {
		t.Fatalf("runDeploy err = %v; out=%s", err, out)
	}

	p.seen.mutex.Lock()
	defer p.seen.mutex.Unlock()
	if len(p.seen.asked) != 1 || p.seen.asked[0].Tier != environment.TierProduction || p.seen.asked[0].ReportFailure == nil {
		t.Fatalf("the provider was asked %d times to forward, want once in production with a way to report a failed forward", len(p.seen.asked))
	}
}

func TestAPreBuildWhoseForwardFailsWhileItRunsStopsTheDeployBeforeAnythingIsPromotedAndSaysWhy(t *testing.T) {
	p := setUpPreBuildProject(t, `"sleep 1"`)
	p.fixture.Provider.WithHooks(func(h *provider.Hooks) {
		forward := h.ForwardPorts
		h.ForwardPorts = func(ctx context.Context, req provider.PortForwardRequest, said progress.Log) ([]provider.PortForward, error) {
			forwards, err := forward(ctx, req, said)
			req.ReportFailure(errors.New("the bastion task stopped: Essential container in task exited"))
			return forwards, err
		}
	})

	out, err := p.deploy(t, deployOptions{yes: true})

	if err == nil || !strings.Contains(err.Error(), "the bastion task stopped") {
		t.Fatalf("runDeploy err = %v, want the failed forward's reason; out=%s", err, out)
	}
	if slices.Contains(p.procedures(), contractv1connect.ProviderServiceDeployProcedure) {
		t.Errorf("the provider was called %v, want nothing promoted", p.procedures())
	}
}

func TestAPreBuildRunsWithoutTheBindingsOfResourcesNoPortReachesAndSaysItIsThePreBuildThatGoesWithout(t *testing.T) {
	p := setUpPreBuildProject(t, `"true"`)
	writeConfigWithLifecycle(t, p.fixture.Root, fmt.Sprintf(`"touch %s"`, p.marker))
	clitest.WriteFile(t, filepath.Join(p.fixture.Root, "shared", "storage.ts"), `
import { declareBucket } from "./declare.js";

export const files = declareBucket("files");
`)
	clitest.WriteFile(t, filepath.Join(p.fixture.Root, "shared", "index.ts"), `
export * from "./db.js";
export * from "./storage.js";
`)

	out, err := p.deploy(t, deployOptions{yes: true})

	if err != nil {
		t.Fatalf("runDeploy err = %v; out=%s", err, out)
	}
	if !p.ran(t) {
		t.Error("the preBuild did not run, though the provider forwarded every port it can")
	}
	if !strings.Contains(out, "lifecycle.preBuild goes without the bindings of files,") || strings.Contains(out, "The build goes without") {
		t.Errorf("the deploy said %q, want it to say the preBuild, which no app's build shares, goes without files", out)
	}
}

func TestAPreBuildDoesNotRunOnAnEphemeralPreviewUnlessItsPreviewsPolicyIsAll(t *testing.T) {
	cases := map[string]bool{
		`"touch MARKER"`: false,
		`{ command: "touch MARKER", previews: "persistent" }`: false,
		`{ command: "touch MARKER", previews: "none" }`:       false,
		`{ command: "touch MARKER", previews: "all" }`:        true,
	}
	for lifecycle, want := range cases {
		t.Run(lifecycle, func(t *testing.T) {
			dependencies := newTestDependencies()
			stubGit(&dependencies, "feature/login", "")
			stubBuild(&dependencies, apiFunction())
			fixture := setUpPreviewProject(t)
			marker := filepath.Join(t.TempDir(), "marker")
			writeUsageMonorepo(t, fixture.Root, "  lifecycle: { preBuild: "+strings.ReplaceAll(lifecycle, "MARKER", marker)+" },\n")
			forwardingPorts(t, fixture)

			previewUp(t, fixture, dependencies, previewUpOptions{})

			if _, err := os.Stat(marker); (err == nil) != want {
				t.Errorf("the preBuild ran on an ephemeral preview = %v, want %v", err == nil, want)
			}
		})
	}
}

func TestAPreBuildRunsOnAPersistentPreviewUnlessItsPreviewsPolicyIsNone(t *testing.T) {
	cases := map[string]bool{
		`"touch MARKER"`: true,
		`{ command: "touch MARKER", previews: "none" }`: false,
	}
	for lifecycle, want := range cases {
		t.Run(lifecycle, func(t *testing.T) {
			dependencies := newTestDependencies()
			stubBuild(&dependencies, apiFunction())
			fixture := setUpPreviewProject(t)
			marker := filepath.Join(t.TempDir(), "marker")
			writeUsageMonorepo(t, fixture.Root, "  lifecycle: { preBuild: "+strings.ReplaceAll(lifecycle, "MARKER", marker)+" },\n")
			forwardingPorts(t, fixture)

			previewUp(t, fixture, dependencies, previewUpOptions{name: "staging", persistent: true})

			if _, err := os.Stat(marker); (err == nil) != want {
				t.Errorf("the preBuild ran on a persistent preview = %v, want %v", err == nil, want)
			}
		})
	}
}

func TestADryDeployListsThePreBuildInItsPlanAndDoesNotRunIt(t *testing.T) {
	p := setUpPreBuildProject(t, `"true"`)
	writeConfigWithLifecycle(t, p.fixture.Root, fmt.Sprintf(`"touch %s"`, p.marker))

	out, err := p.deploy(t, deployOptions{yes: true, dry: true})

	if err != nil {
		t.Fatalf("runDeploy err = %v; out=%s", err, out)
	}
	if p.ran(t) {
		t.Error("a dry deploy ran the preBuild")
	}
	if !strings.Contains(out, "lifecycle.preBuild") || !strings.Contains(out, "touch "+p.marker) {
		t.Errorf("the dry deploy said %q, want the preBuild and its command listed", out)
	}
}

func TestAPrebuiltDeploySkipsThePreBuildAndSaysWhy(t *testing.T) {
	p := setUpPreBuildProject(t, `"true"`)
	writeConfigWithLifecycle(t, p.fixture.Root, fmt.Sprintf(`"touch %s"`, p.marker))
	if out, err := p.deploy(t, deployOptions{yes: true}); err != nil {
		t.Fatalf("the first deploy err = %v; out=%s", err, out)
	}
	if err := os.Remove(p.marker); err != nil {
		t.Fatal(err)
	}

	out, err := p.deploy(t, deployOptions{yes: true, prebuilt: true})

	if err != nil {
		t.Fatalf("runDeploy err = %v; out=%s", err, out)
	}
	if p.ran(t) {
		t.Error("a prebuilt deploy ran the preBuild")
	}
	if !strings.Contains(out, "--prebuilt") || !strings.Contains(out, "lifecycle.preBuild") {
		t.Errorf("the deploy said %q, want it to say --prebuilt skips lifecycle.preBuild", out)
	}
}

func TestADeployWarnsOnceThatNpmRunsAPrebuildScriptPerAppWhenAPreBuildIsSet(t *testing.T) {
	p := setUpPreBuildProject(t, `"true"`)
	root := p.fixture.Root
	clitest.WriteFile(t, filepath.Join(root, "apps", "api", "package.json"), `{"scripts":{"prebuild":"drizzle-kit migrate"}}`)
	clitest.WriteFile(t, filepath.Join(root, "apps", "worker", "package.json"), `{"scripts":{"prebuild":"drizzle-kit migrate"}}`)
	writeConfig(t, root, `  apps: [
    { name: "api", path: "apps/api", framework: "node" },
    { name: "worker", path: "apps/worker", framework: "node" },
  ],
  lifecycle: { preBuild: "true" },
`)

	out, err := p.deploy(t, deployOptions{yes: true})

	if err != nil {
		t.Fatalf("runDeploy err = %v; out=%s", err, out)
	}
	if got := strings.Count(out, "prebuild script"); got != 1 {
		t.Errorf("the deploy warned about a prebuild script %d times in %q, want once", got, out)
	}
	if !strings.Contains(out, "api") || !strings.Contains(out, "worker") {
		t.Errorf("the deploy said %q, want the warning to name both apps", out)
	}
}

func TestADeployWithoutAPreBuildDoesNotWarnAboutAPrebuildScript(t *testing.T) {
	dependencies := newTestDependencies()
	stubBuild(&dependencies, apiFunction())
	fixture := setUpDeployProject(t)
	writeUsageMonorepo(t, fixture.Root, "")
	clitest.WriteFile(t, filepath.Join(fixture.Root, "apps", "api", "package.json"), `{"scripts":{"prebuild":"x"}}`)

	out := deployedSaying(t, dependencies, fixture, deployOptions{yes: true})

	if strings.Contains(out, "prebuild script") {
		t.Errorf("the deploy said %q, want no warning without lifecycle.preBuild", out)
	}
}

func TestAFailedPreBuildLeavesNoLiveDirectoryBehind(t *testing.T) {
	p := setUpPreBuildProject(t, `"true"`)
	record := filepath.Join(t.TempDir(), "dir")
	writeConfigWithLifecycle(t, p.fixture.Root, fmt.Sprintf(`"echo \"$OCEL_LIVE_DIR\" > %s; exit 1"`, record))

	if _, err := p.deploy(t, deployOptions{yes: true}); err == nil {
		t.Fatal("runDeploy err = nil, want the failure")
	}

	dir := strings.TrimSpace(readOrEmpty(t, record))
	if dir == "" {
		t.Fatal("the preBuild was handed no live dir")
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the live dir %s survives the failed preBuild (stat err %v)", dir, err)
	}
}

func writeConfigWithLifecycle(t *testing.T, root, preBuild string) {
	t.Helper()
	writeConfig(t, root, `  apps: [{ name: "api", path: "apps/api", framework: "node" }],
  lifecycle: { preBuild: `+preBuild+` },
`)
}

func readOrEmpty(t *testing.T, path string) string {
	t.Helper()
	read, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return ""
	}
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(read)
}

func TestAPreBuildNamingAnAppAlsoReceivesThatAppsVariables(t *testing.T) {
	fixture := setUpVariablesProject(t, `[{"key":"LIVE_KEY","class":"VARIABLE_CLASS_SECRET","required":true},{"key":"BAKED_KEY","class":"VARIABLE_CLASS_PLAIN","required":true}]`)
	envSet(t, fixture, "LIVE_KEY", "sk_live_value", envOptions{})
	envSet(t, fixture, "BAKED_KEY", "baked_value", envOptions{})
	writeRootApp(t, fixture.Root)
	seen := filepath.Join(t.TempDir(), "seen")
	writeConfig(t, fixture.Root, fmt.Sprintf(`  lifecycle: { preBuild: { app: %q, command: "echo \"$BAKED_KEY\" > %s; cat \"$OCEL_LIVE_DIR/LIVE_KEY\" >> %s" } },
`, clitest.FixtureSlug, seen, seen))

	dependencies := newTestDependencies()
	stubBuild(&dependencies, nil)

	out, err := deployWith(t, dependencies, fixture, deployOptions{yes: true})

	if err != nil {
		t.Fatalf("runDeploy err = %v; out=%s", err, out)
	}
	if got := readOrEmpty(t, seen); got != "baked_value\nsk_live_value" {
		t.Errorf("the preBuild saw %q, want the app's plain value in its environment and its secret in the live dir", got)
	}
}

func TestAPreBuildNamingAnAppThatDeclaresANameTheBindingProxyIsDeliveredUnderIsRefusedBeforeItRuns(t *testing.T) {
	fixture := setUpVariablesProject(t, fmt.Sprintf(`[{"key":%q,"class":"VARIABLE_CLASS_PLAIN","required":true}]`, localrpc.SessionTokenEnvVar))
	envSet(t, fixture, localrpc.SessionTokenEnvVar, "mine", envOptions{})
	writeRootApp(t, fixture.Root)
	ran := filepath.Join(t.TempDir(), "ran")
	writeConfig(t, fixture.Root, fmt.Sprintf(`  lifecycle: { preBuild: { app: %q, command: "touch %s" } },
`, clitest.FixtureSlug, ran))

	dependencies := newTestDependencies()
	stubBuild(&dependencies, nil)

	out, err := deployWith(t, dependencies, fixture, deployOptions{yes: true})

	if err == nil || !strings.Contains(err.Error(), localrpc.SessionTokenEnvVar) {
		t.Errorf("runDeploy err = %v, want the preBuild refused by the name it would lose to the binding proxy; out=%s", err, out)
	}
	if _, statErr := os.Stat(ran); statErr == nil {
		t.Error("the preBuild ran though its app declares a name the binding proxy is delivered under")
	}
}

func TestAPreBuildIsHandedTheBindingProxyAndNeverShowsItsSessionToken(t *testing.T) {
	p := setUpPreBuildProject(t, `"true"`)
	servingBindingProxy(t, p.fixture)
	writeNextDatabaseAndBucketProject(t, p.fixture.Root)
	seen := filepath.Join(t.TempDir(), "runtime")
	writeConfigWithLifecycle(t, p.fixture.Root, fmt.Sprintf("%q", fmt.Sprintf(`echo "$%s $%s" > %s; echo "token is $%s"`, processenv.RuntimeAddressEnvVar, localrpc.SessionTokenEnvVar, seen, localrpc.SessionTokenEnvVar)))

	out, err := p.deploy(t, deployOptions{yes: true})
	if err != nil {
		t.Fatalf("runDeploy err = %v; out=%s", err, out)
	}

	if got := strings.TrimSpace(readOrEmpty(t, seen)); got != "http://127.0.0.1:41999 proxy-token" {
		t.Errorf("the preBuild saw the runtime env %q, want the binding proxy the provider served", got)
	}
	if strings.Contains(out, "proxy-token") {
		t.Errorf("the deploy showed %q, want the session token hidden", out)
	}
}

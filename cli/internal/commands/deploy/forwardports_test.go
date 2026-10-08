package deploy

import (
	"bytes"
	"context"
	"errors"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/localrpc"
	"github.com/ocelhq/ocel/pkg/processenv"
	"github.com/ocelhq/ocel/pkg/progress"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/statedir"
)

const (
	forwardedPostgresKey = "OCEL_RESOURCE_POSTGRES_main"
	forwardTeardown      = 200 * time.Millisecond
)

type forwardsSeen struct {
	mutex        sync.Mutex
	asked        []provider.PortForwardRequest
	open         int
	openAtDeploy []int
}

func (s *forwardsSeen) openNow() int {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return s.open
}

func forwardingPorts(t *testing.T, fixture clitest.FakeProject) *forwardsSeen {
	t.Helper()
	seen := &forwardsSeen{}
	fixture.Provider.WithHooks(func(h *provider.Hooks) {
		h.ForwardPorts = func(_ context.Context, req provider.PortForwardRequest, _ progress.Log) ([]provider.PortForward, error) {
			seen.mutex.Lock()
			defer seen.mutex.Unlock()
			seen.asked = append(seen.asked, req)
			forwards := make([]provider.PortForward, 0, len(req.Bindings))
			for _, binding := range req.Bindings {
				seen.open++
				forwards = append(forwards, provider.PortForward{Binding: binding.Name, LocalAddress: "127.0.0.1:41234", Close: func() {
					time.Sleep(forwardTeardown)
					seen.mutex.Lock()
					defer seen.mutex.Unlock()
					seen.open--
				}})
			}
			return forwards, nil
		}
		preflight := h.PreflightDeploy
		h.PreflightDeploy = func(ctx context.Context, pre provider.DeployPreflight) error {
			seen.mutex.Lock()
			seen.openAtDeploy = append(seen.openAtDeploy, seen.open)
			seen.mutex.Unlock()
			if preflight == nil {
				return nil
			}
			return preflight(ctx, pre)
		}
	})
	return seen
}

func writeNextUsageProject(t *testing.T, root, build string) {
	t.Helper()
	clitest.WriteUsageMonorepo(t, root)
	writeConfig(t, root, `  apps: [{ name: "web", path: "apps/api", framework: "next", compute: "serverless"`+build+` }],
`)
}

func capturingBuild(t *testing.T, dependencies *Dependencies) *build.AppVariables {
	t.Helper()
	stubBuild(dependencies, []build.Function{
		{Route: "index", Framework: buildoutput.Framework{Name: "next"}, EntryFile: "index.js", ArtifactPath: "output/web", App: "web"},
	})
	built := dependencies.BuildApps
	seen := &build.AppVariables{}
	dependencies.BuildApps = func(ctx context.Context, cfg *project.Project, variables map[string]build.AppVariables, archs map[string]string, workers build.HostedWorkers, host build.Host, log build.Log) (build.Output, error) {
		*seen = variables["web"]
		return built(ctx, cfg, variables, archs, workers, host, log)
	}
	return seen
}

func TestADeployBuildsAnAppWithTheBindingsOfWhatItUsesPointedAtPortForwardsAndClosesThemBeforeItDeploys(t *testing.T) {
	dependencies := newTestDependencies()
	fixture := setUpDeployProject(t)
	writeNextUsageProject(t, fixture.Root, "")
	seen := forwardingPorts(t, fixture)
	built := capturingBuild(t, &dependencies)

	deployed(t, dependencies, fixture, deployOptions{yes: true})

	raw, ok := built.Live[forwardedPostgresKey]
	if !ok {
		t.Fatalf("the build was handed %v to read from its live dir, want %s", slices.Sorted(maps.Keys(built.Live)), forwardedPostgresKey)
	}
	var delivered bindingsv1.Binding
	if err := protojson.Unmarshal([]byte(raw), &delivered); err != nil {
		t.Fatalf("the build read %s as %q, want a binding record: %v", forwardedPostgresKey, raw, err)
	}
	if got := delivered.GetPostgres(); got.GetHost() != "127.0.0.1" || got.GetPort() != 41234 || got.GetTlsServerName() == "" {
		t.Errorf("the build read main on %s:%d verified as %q, want the forward at 127.0.0.1:41234 verified as the database's own host", got.GetHost(), got.GetPort(), got.GetTlsServerName())
	}
	for key := range built.Env {
		if strings.HasPrefix(key, processenv.ResourceEnvVarPrefix) {
			t.Errorf("the build env holds %s, want bindings only in the live directory, never the environment", key)
		}
	}

	sent := clitest.RequestsTo[*contractv1.ForwardPortsRequest](t, fixture.Requests, contractv1connect.ProviderServiceForwardPortsProcedure)
	if len(sent) != 1 || !slices.Equal(sent[0].GetBindings(), []string{"db--main"}) {
		t.Errorf("the CLI asked to forward %v, want the one binding the built app uses", sent)
	}
	seen.mutex.Lock()
	deployedWith := slices.Clone(seen.openAtDeploy)
	seen.mutex.Unlock()
	if !slices.Equal(deployedWith, []int{0}) {
		t.Errorf("the deploy began with %v forwards still open, want it to begin once, after every forward the build used was closed", deployedWith)
	}
}

func TestAnAppWhoseBuildTakesNoBindingsIsBuiltWithoutForwardingAPort(t *testing.T) {
	dependencies := newTestDependencies()
	fixture := setUpDeployProject(t)
	writeNextUsageProject(t, fixture.Root, `, build: { bindings: false }`)
	forwardingPorts(t, fixture)
	built := capturingBuild(t, &dependencies)

	deployed(t, dependencies, fixture, deployOptions{yes: true})

	if _, ok := built.Live[forwardedPostgresKey]; ok {
		t.Errorf("an app that opted out was built with %s", forwardedPostgresKey)
	}
	if sent := clitest.RequestsTo[*contractv1.ForwardPortsRequest](t, fixture.Requests, contractv1connect.ProviderServiceForwardPortsProcedure); len(sent) != 0 {
		t.Errorf("the CLI forwarded ports for %v, want none for an app whose build takes no bindings", sent)
	}
}

func deployedSaying(t *testing.T, dependencies Dependencies, fixture clitest.FakeProject, opts deployOptions) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runDeploy(context.Background(), dependencies, fixture.Root, opts, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	return stdout.String()
}

func TestTheForwardingStepNamesTheResourcesAsTheAppDeclaresThem(t *testing.T) {
	dependencies := newTestDependencies()
	fixture := setUpDeployProject(t)
	writeNextUsageProject(t, fixture.Root, "")
	fixture.Provider.WithHooks(func(h *provider.Hooks) {
		h.ForwardPorts = func(context.Context, provider.PortForwardRequest, progress.Log) ([]provider.PortForward, error) {
			return nil, errors.New("the box is unreachable")
		}
	})
	capturingBuild(t, &dependencies)

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	err := runDeploy(context.Background(), dependencies, fixture.Root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))

	if err == nil {
		t.Fatal("runDeploy() = nil, want the failed forward to stop the deploy")
	}
	if said := stdout.String(); !strings.Contains(said, "ports to main ") || strings.Contains(said, "ports to db--main") {
		t.Errorf("the deploy said %q, want the forwarding step to name main as the app declares it", said)
	}
}

func TestWhatTheProviderSaysWhileForwardingPortsIsShownInTheBuild(t *testing.T) {
	dependencies := newTestDependencies()
	fixture := setUpDeployProject(t)
	writeNextUsageProject(t, fixture.Root, "")
	fixture.Provider.WithHooks(func(h *provider.Hooks) {
		h.ForwardPorts = func(_ context.Context, req provider.PortForwardRequest, progress progress.Log) ([]provider.PortForward, error) {
			progress.Say("Creating Cloud Run service ocel-production-bastion")
			progress.Warn("A connection to db--main failed")
			forwards := make([]provider.PortForward, 0, len(req.Bindings))
			for _, binding := range req.Bindings {
				forwards = append(forwards, provider.PortForward{Binding: binding.Name, LocalAddress: "127.0.0.1:41234"})
			}
			return forwards, nil
		}
	})
	capturingBuild(t, &dependencies)

	said := deployedSaying(t, dependencies, fixture, deployOptions{yes: true})

	for _, want := range []string{"Creating Cloud Run service ocel-production-bastion", "A connection to db--main failed"} {
		if !strings.Contains(said, want) {
			t.Errorf("the deploy said %q, want %q from the provider among it", said, want)
		}
	}
}

func TestAProviderThatForwardsNoPortIsNotAskedToAndOpensNoForwardingStep(t *testing.T) {
	dependencies := newTestDependencies()
	fixture := setUpDeployProject(t)
	writeNextUsageProject(t, fixture.Root, "")
	built := capturingBuild(t, &dependencies)

	said := deployedSaying(t, dependencies, fixture, deployOptions{yes: true})

	if sent := clitest.RequestsTo[*contractv1.ForwardPortsRequest](t, fixture.Requests, contractv1connect.ProviderServiceForwardPortsProcedure); len(sent) != 0 {
		t.Errorf("the CLI asked a provider that forwards no port to forward %v", sent)
	}
	if strings.Contains(said, "Forwarding ports") || strings.Contains(said, "forwards no port") {
		t.Errorf("the deploy said %q, want no forwarding step on a provider that forwards no port", said)
	}
	if _, ok := built.Live[forwardedPostgresKey]; ok {
		t.Errorf("the build was handed %s, want no bindings when nothing was forwarded", forwardedPostgresKey)
	}
}

func writeNextDatabaseAndBucketProject(t *testing.T, root string) {
	t.Helper()
	writeNextUsageProject(t, root, "")
	clitest.WriteFile(t, filepath.Join(root, "shared", "storage.ts"), `
import { declareBucket } from "./declare.js";

export const files = declareBucket("files");
`)
	clitest.WriteFile(t, filepath.Join(root, "shared", "index.ts"), `
export * from "./db.js";
export * from "./storage.js";
`)
	clitest.WriteFile(t, filepath.Join(root, "apps", "api", "src", "server.ts"), `
import { db, files } from "../../../shared/index.js";

export function handler() {
  return db.name + files.name;
}
`)
}

func TestTheProviderDecidesWhichBindingsItForwardsAndTheBuildGoesWithoutTheRest(t *testing.T) {
	dependencies := newTestDependencies()
	fixture := setUpDeployProject(t)
	writeNextDatabaseAndBucketProject(t, fixture.Root)
	forwardingPorts(t, fixture)
	built := capturingBuild(t, &dependencies)

	said := deployedSaying(t, dependencies, fixture, deployOptions{yes: true})

	sent := clitest.RequestsTo[*contractv1.ForwardPortsRequest](t, fixture.Requests, contractv1connect.ProviderServiceForwardPortsProcedure)
	if len(sent) != 1 || len(sent[0].GetBindings()) != 2 {
		t.Fatalf("the CLI asked to forward %v, want every binding the built app uses, left to the provider to forward or not", sent)
	}
	if _, ok := built.Live[forwardedPostgresKey]; !ok {
		t.Error("the build was not handed main, which the provider forwarded")
	}
	if !strings.Contains(said, "goes without the bindings of files,") || strings.Contains(said, "files--files") {
		t.Errorf("the deploy said %q, want it to say the build goes without files, named as the app declares it", said)
	}
}

func TestAnEphemeralPreviewBuildsWithTheTiersPublishedBindingsPointedAtPortForwards(t *testing.T) {
	dependencies := newTestDependencies()
	stubGit(&dependencies, "feature/login", "")
	fixture := setUpPreviewProject(t)
	writeNextUsageProject(t, fixture.Root, "")
	forwardingPorts(t, fixture)
	built := capturingBuild(t, &dependencies)

	previewUp(t, fixture, dependencies, previewUpOptions{})

	sent := clitest.RequestsTo[*contractv1.ForwardPortsRequest](t, fixture.Requests, contractv1connect.ProviderServiceForwardPortsProcedure)
	if len(sent) != 1 || !slices.Equal(sent[0].GetBindings(), []string{"db--main"}) || sent[0].GetEnvironment().GetLifecycle() != environmentv1.Lifecycle_LIFECYCLE_EPHEMERAL {
		t.Fatalf("the CLI asked to forward %v, want db--main for the ephemeral preview, which builds on the tier's published bindings", sent)
	}
	if _, ok := built.Live[forwardedPostgresKey]; !ok {
		t.Error("the ephemeral preview was built without main's binding")
	}
}

func TestADryDeployBuildsWithForwardedBindingsWhenEverythingItUsesIsAlreadyDeployed(t *testing.T) {
	dependencies := newTestDependencies()
	fixture := setUpDeployProject(t)
	writeNextUsageProject(t, fixture.Root, "")
	forwardingPorts(t, fixture)
	built := capturingBuild(t, &dependencies)
	deployed(t, dependencies, fixture, deployOptions{yes: true})
	*built = build.AppVariables{}

	deployedSaying(t, dependencies, fixture, deployOptions{dry: true})

	if sent := clitest.RequestsTo[*contractv1.ForwardPortsRequest](t, fixture.Requests, contractv1connect.ProviderServiceForwardPortsProcedure); len(sent) != 2 {
		t.Fatalf("the CLI sent %d ForwardPorts requests over a deploy and a dry run, want one each", len(sent))
	}
	if _, ok := built.Live[forwardedPostgresKey]; !ok {
		t.Error("the dry run was built without main's binding, which the earlier deploy published")
	}
}

func TestADryDeployOfResourcesNotYetDeployedBuildsWithoutBindingsAndSaysSo(t *testing.T) {
	dependencies := newTestDependencies()
	fixture := setUpDeployProject(t)
	writeNextUsageProject(t, fixture.Root, "")
	forwardingPorts(t, fixture)
	built := capturingBuild(t, &dependencies)

	said := deployedSaying(t, dependencies, fixture, deployOptions{dry: true})

	if _, ok := built.Live[forwardedPostgresKey]; ok {
		t.Errorf("the dry run was built with %s, want no bindings for resources nothing has deployed", forwardedPostgresKey)
	}
	if !strings.Contains(said, "The build goes without the bindings of main: ") || !strings.Contains(said, "not deployed yet") {
		t.Errorf("the dry run said %q, want it to say the build goes without main's binding", said)
	}
}

func TestADeployLeavesNoForwardedBindingsPasswordInTheProjectsOcelDirectory(t *testing.T) {
	dependencies := newTestDependencies()
	fixture := setUpDeployProject(t)
	writeNextUsageProject(t, fixture.Root, "")
	forwardingPorts(t, fixture)
	built := capturingBuild(t, &dependencies)

	deployed(t, dependencies, fixture, deployOptions{yes: true})

	var delivered bindingsv1.Binding
	if err := protojson.Unmarshal([]byte(built.Live[forwardedPostgresKey]), &delivered); err != nil || delivered.GetPostgres().GetPassword() == "" {
		t.Fatalf("the build was handed %q, want main's binding with its password", built.Live[forwardedPostgresKey])
	}
	password := delivered.GetPostgres().GetPassword()
	err := filepath.WalkDir(filepath.Join(fixture.Root, statedir.Name), func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(raw), password) {
			t.Errorf("%s holds main's password, want it only in the live directory the build read", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestAnEphemeralPreviewOfResourcesNothingPublishedBuildsWithoutTheirBindingsAndLeavesTheRefusalToItsDeploy(t *testing.T) {
	dependencies := newTestDependencies()
	stubGit(&dependencies, "feature/login", "")
	fixture := setUpPreviewProject(t)
	writeNextUsageProject(t, fixture.Root, "")
	forwardingPorts(t, fixture)
	built := capturingBuild(t, &dependencies)

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	err := runPreviewUp(context.Background(), dependencies, fixture.Root, previewUpOptions{}, &stdout, &stderr, strings.NewReader(""))

	if err == nil || !strings.Contains(err.Error(), "--persistent") {
		t.Errorf("runPreviewUp() = %v, want the deploy's own refusal of a preview nothing covers", err)
	}
	if _, ok := built.Live[forwardedPostgresKey]; ok {
		t.Errorf("the preview was built with %s, which nothing published", forwardedPostgresKey)
	}
	if said := stdout.String(); !strings.Contains(said, "The build goes without the bindings of main") {
		t.Errorf("the preview said %q, want it to say the build goes without main", said)
	}
}

func TestADeployWhoseForwardIsNotReadyBuildsWithoutThatBindingAndDeploysSayingWhy(t *testing.T) {
	dependencies := newTestDependencies()
	fixture := setUpDeployProject(t)
	writeNextUsageProject(t, fixture.Root, "")
	fixture.Provider.WithHooks(func(h *provider.Hooks) {
		h.ForwardPorts = func(context.Context, provider.PortForwardRequest, progress.Log) ([]provider.PortForward, error) {
			return nil, refusal.Refuse(refusal.CodeNotReady, "container db holds no address on any network, so it is not running")
		}
	})
	built := capturingBuild(t, &dependencies)

	said := deployedSaying(t, dependencies, fixture, deployOptions{yes: true})

	if _, ok := built.Live[forwardedPostgresKey]; ok {
		t.Errorf("the build was handed %s, which nothing forwarded", forwardedPostgresKey)
	}
	if !strings.Contains(said, "The build goes without the bindings of main: ") || !strings.Contains(said, "is not running") {
		t.Errorf("the deploy said %q, want it to say the build goes without main and why", said)
	}
}

func TestANextAppBuiltAsAnImageIsBuiltWithTheBindingsOfWhatItUsesPointedAtPortForwards(t *testing.T) {
	dependencies := newTestDependencies()
	fixture := setUpDeployProject(t)
	clitest.WriteUsageMonorepo(t, fixture.Root)
	writeConfig(t, fixture.Root, `  apps: [{ name: "web", path: "apps/api", framework: "next", compute: "container" }],
`)
	forwardingPorts(t, fixture)
	dependencies.RefuseUnbuildableImages = func(context.Context, *run.Span, *project.Project, map[string]string) error { return nil }
	var live map[string]string
	dependencies.BuildApps = func(_ context.Context, _ *project.Project, variables map[string]build.AppVariables, _ map[string]string, _ build.HostedWorkers, _ build.Host, _ build.Log) (build.Output, error) {
		live = variables["web"].Live
		return build.Output{}, errors.New("the image is not built in this test")
	}

	deployErr := runDeploy(context.Background(), dependencies, fixture.Root, deployOptions{yes: true}, io.Discard, io.Discard, strings.NewReader(""))

	if live == nil {
		t.Fatalf("the deploy never reached the build with a live value: %v", deployErr)
	}
	if _, ok := live[forwardedPostgresKey]; !ok {
		t.Errorf("the image build was handed %v to read from its live dir, want %s", slices.Sorted(maps.Keys(live)), forwardedPostgresKey)
	}
	sent := clitest.RequestsTo[*contractv1.ForwardPortsRequest](t, fixture.Requests, contractv1connect.ProviderServiceForwardPortsProcedure)
	if len(sent) != 1 || !slices.Equal(sent[0].GetBindings(), []string{"db--main"}) {
		t.Errorf("the CLI asked to forward %v, want the one binding the image build uses", sent)
	}
}

func TestANextAppBuiltAsAnImageIsHandedTheBindingProxyAndTheBindingsItsBuildReads(t *testing.T) {
	dependencies := newTestDependencies()
	fixture := setUpDeployProject(t)
	writeNextDatabaseAndBucketProject(t, fixture.Root)
	writeConfig(t, fixture.Root, `  apps: [{ name: "web", path: "apps/api", framework: "next", compute: "container" }],
`)
	servingBindingProxy(t, fixture)
	dependencies.RefuseUnbuildableImages = func(context.Context, *run.Span, *project.Project, map[string]string) error { return nil }
	var built *build.AppVariables
	dependencies.BuildApps = func(_ context.Context, _ *project.Project, variables map[string]build.AppVariables, _ map[string]string, _ build.HostedWorkers, _ build.Host, _ build.Log) (build.Output, error) {
		web := variables["web"]
		built = &web
		return build.Output{}, errors.New("the image is not built in this test")
	}

	_ = runDeploy(context.Background(), dependencies, fixture.Root, deployOptions{yes: true}, io.Discard, io.Discard, strings.NewReader(""))

	if built == nil {
		t.Fatal("the deploy never reached the build")
	}
	wantProxy := map[string]string{processenv.RuntimeAddressEnvVar: "http://127.0.0.1:41999", localrpc.SessionTokenEnvVar: "proxy-token"}
	if !maps.Equal(built.BindingProxyEnv, wantProxy) {
		t.Errorf("the image build was handed the binding proxy %v, want the one the provider served", built.BindingProxyEnv)
	}
	for _, key := range []string{forwardedPostgresKey, "OCEL_RESOURCE_BUCKET_files"} {
		if _, ok := built.Live[key]; !ok {
			t.Errorf("the image build was handed %v to read, want %s", slices.Sorted(maps.Keys(built.Live)), key)
		}
	}
}

func servingBindingProxy(t *testing.T, fixture clitest.FakeProject) *forwardsSeen {
	t.Helper()
	seen := forwardingPorts(t, fixture)
	fixture.Provider.WithHooks(func(h *provider.Hooks) {
		h.ServeBindingProxy = func(_ context.Context, req provider.BindingProxyRequest, _ progress.Log) (provider.BindingProxy, error) {
			seen.mutex.Lock()
			defer seen.mutex.Unlock()
			seen.open++
			return provider.BindingProxy{Address: "http://127.0.0.1:41999", SessionToken: "proxy-token", Close: func() {
				time.Sleep(forwardTeardown)
				seen.mutex.Lock()
				defer seen.mutex.Unlock()
				seen.open--
			}}, nil
		}
	})
	return seen
}

func TestADeployBuildsAnAppWithTheBindingProxyAndTheBucketRecordItBindsAndClosesTheProxyBeforeItDeploys(t *testing.T) {
	dependencies := newTestDependencies()
	fixture := setUpDeployProject(t)
	writeNextDatabaseAndBucketProject(t, fixture.Root)
	seen := servingBindingProxy(t, fixture)
	built := capturingBuild(t, &dependencies)

	said := deployedSaying(t, dependencies, fixture, deployOptions{yes: true})

	wantRuntime := map[string]string{processenv.RuntimeAddressEnvVar: "http://127.0.0.1:41999", localrpc.SessionTokenEnvVar: "proxy-token"}
	if !maps.Equal(built.BindingProxyEnv, wantRuntime) {
		t.Errorf("the build was handed the runtime env %v, want the binding proxy the provider served", built.BindingProxyEnv)
	}
	if _, ok := built.Live["OCEL_RESOURCE_BUCKET_files"]; !ok {
		t.Errorf("the build was handed %v to read from its live dir, want the files bucket beside main", slices.Sorted(maps.Keys(built.Live)))
	}
	if strings.Contains(said, "goes without the bindings of files") {
		t.Errorf("the deploy said %q, want no bucket left out of the build", said)
	}
	if strings.Contains(said, "proxy-token") {
		t.Errorf("the deploy said %q, want the session token never shown", said)
	}
	seen.mutex.Lock()
	deployedWith := slices.Clone(seen.openAtDeploy)
	seen.mutex.Unlock()
	if !slices.Equal(deployedWith, []int{0}) {
		t.Errorf("the deploy began with %v forwards or proxies still open, want it to begin after the build closed them", deployedWith)
	}
}

func TestAnAppWhoseBuildTakesNoBindingsIsHandedNoBindingProxy(t *testing.T) {
	dependencies := newTestDependencies()
	fixture := setUpDeployProject(t)
	writeNextDatabaseAndBucketProject(t, fixture.Root)
	writeConfig(t, fixture.Root, `  apps: [{ name: "web", path: "apps/api", framework: "next", compute: "serverless", build: { bindings: false } }],
`)
	servingBindingProxy(t, fixture)
	built := capturingBuild(t, &dependencies)

	deployed(t, dependencies, fixture, deployOptions{yes: true})

	if len(built.BindingProxyEnv) != 0 {
		t.Errorf("an app that opted out was handed the runtime env %v", built.BindingProxyEnv)
	}
}

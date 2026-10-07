package deploy

import (
	"bytes"
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"sync"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/processenv"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
)

const forwardedPostgresKey = "OCEL_RESOURCE_POSTGRES_main"

type forwardsSeen struct {
	mu           sync.Mutex
	asked        []provider.PortForwardRequest
	closedBefore []string
}

func forwardingPorts(t *testing.T, fixture clitest.FakeProject) *forwardsSeen {
	t.Helper()
	seen := &forwardsSeen{}
	fixture.Provider.WithHooks(func(h *provider.Hooks) {
		h.ForwardPorts = func(ctx context.Context, req provider.PortForwardRequest) ([]provider.PortForward, error) {
			seen.mu.Lock()
			seen.asked = append(seen.asked, req)
			seen.mu.Unlock()
			go func() {
				<-ctx.Done()
				seen.mu.Lock()
				defer seen.mu.Unlock()
				seen.closedBefore = fixture.Requests.Procedures()
			}()
			forwards := make([]provider.PortForward, 0, len(req.Bindings))
			for _, binding := range req.Bindings {
				forwards = append(forwards, provider.PortForward{Binding: binding.Name, LocalAddress: "127.0.0.1:41234"})
			}
			return forwards, nil
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
	seen.mu.Lock()
	closedBefore := slices.Clone(seen.closedBefore)
	seen.mu.Unlock()
	if closedBefore == nil || slices.Contains(closedBefore, contractv1connect.ProviderServiceDeployProcedure) {
		t.Errorf("the forwards closed after %v, want them closed once the build ended and before the deploy", closedBefore)
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
		h.ForwardPorts = func(context.Context, provider.PortForwardRequest) ([]provider.PortForward, error) {
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

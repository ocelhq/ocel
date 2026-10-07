package deploy

import (
	"context"
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

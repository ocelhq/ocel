package providerserver_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/providerserver"
)

func servingRoot(t *testing.T, app string, hosting buildoutput.Hosting, manifest []byte) string {
	t.Helper()
	root := t.TempDir()
	dir := buildoutput.AppRoot(root, app)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	hosting.Version = buildoutput.HostingVersion
	raw, err := json.Marshal(hosting)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, buildoutput.HostingFile), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if manifest != nil {
		if err := os.WriteFile(filepath.Join(dir, edge.NextRouteTableFile), manifest, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func servingQuery(root, app, framework string) providerserver.AppServingInput {
	return providerserver.AppServingInput{
		Root:       root,
		Project:    "shop",
		App:        app,
		Framework:  framework,
		Stack:      naming.AppStack("production", app, naming.NewReleaseToken("dep1", "fp1")),
		Coordinate: naming.Coordinate{Project: "shop", Env: "production", App: app, Release: naming.NewReleaseToken("dep1", "fp1")},
	}
}

func TestEveryAppIncludesTheAssetPrefixAndBytecodeCacheItServesFrom(t *testing.T) {
	facts, err := providerserver.AppServingFor(servingQuery(t.TempDir(), "web", "astro"))
	if err != nil {
		t.Fatalf("AppServingFor() = %v", err)
	}
	if facts.AssetPrefix == "" {
		t.Error("an app with no asset prefix serves its static files from nowhere")
	}
	if facts.Bytecode == nil || facts.Bytecode.Prefix == "" {
		t.Fatalf("Bytecode = %+v, want a prefix every framework can warm a cache under", facts.Bytecode)
	}
	if strings.HasSuffix(facts.Bytecode.Prefix, naming.PathSeparator) {
		t.Errorf("Bytecode.Prefix = %q, want it free of the trailing separator a key policy appends", facts.Bytecode.Prefix)
	}
	if facts.Bytecode.Prefix == facts.AssetPrefix {
		t.Error("the bytecode cache and the static assets share a prefix; a deploy would sweep one with the other")
	}
}

func TestABuildThatDeclaresISRAsksForAnISRLedgerWhateverItsFramework(t *testing.T) {
	root := servingRoot(t, "web", buildoutput.Hosting{Framework: buildoutput.FrameworkSvelteKit, ISR: true}, nil)
	declared, err := providerserver.AppServingFor(servingQuery(root, "web", buildoutput.FrameworkSvelteKit))
	if err != nil {
		t.Fatalf("AppServingFor() = %v", err)
	}
	if declared.ISR == nil || declared.ISR.Prefix == "" || declared.ISR.TagNamespace == "" {
		t.Fatalf("ISR = %+v, want the prefix and tag namespace a revalidation writes through", declared.ISR)
	}
	root = servingRoot(t, "web", buildoutput.Hosting{Framework: buildoutput.FrameworkNext}, nil)
	undeclared, err := providerserver.AppServingFor(servingQuery(root, "web", buildoutput.FrameworkNext))
	if err != nil {
		t.Fatalf("AppServingFor() = %v", err)
	}
	if undeclared.ISR != nil {
		t.Errorf("ISR = %+v for a Next build that declares none, want none", undeclared.ISR)
	}
}

func TestAnAppWithNoBuildOutputAsksForAnISRLedgerOnlyWhenItIsNext(t *testing.T) {
	next, err := providerserver.AppServingFor(servingQuery(t.TempDir(), "web", buildoutput.FrameworkNext))
	if err != nil {
		t.Fatalf("AppServingFor() = %v", err)
	}
	if next.ISR == nil {
		t.Fatal("ISR = nil for an unbuilt Next app, want the ledger Next is presumed to use")
	}
	for _, framework := range []string{"astro", buildoutput.FrameworkNode, buildoutput.FrameworkSvelteKit} {
		other, err := providerserver.AppServingFor(servingQuery(t.TempDir(), "web", framework))
		if err != nil {
			t.Fatalf("AppServingFor() = %v", err)
		}
		if other.ISR != nil {
			t.Errorf("ISR = %+v for an unbuilt %s app, want none", other.ISR, framework)
		}
	}
}

func TestAnAppDispatchingAtItsOriginIncludesTheManifestItDispatchesBy(t *testing.T) {
	manifest := []byte(`{"routes":[]}`)
	root := servingRoot(t, "web", buildoutput.Hosting{RouteTable: edge.RouteTableNext, RootFunction: "index"}, manifest)

	facts, err := providerserver.AppServingFor(servingQuery(root, "web", buildoutput.FrameworkNext))
	if err != nil {
		t.Fatalf("AppServingFor() = %v", err)
	}
	if facts.OriginDispatch == nil {
		t.Fatal("OriginDispatch = nil for an app whose build says it dispatches at its origin")
	}
	if facts.OriginDispatch.RootFunction != "index" {
		t.Errorf("OriginDispatch.RootFunction = %q, want the root function the build named", facts.OriginDispatch.RootFunction)
	}
	if got := facts.OriginDispatch.RouteTable; got.Format != edge.RouteTableNext || !bytes.Equal(got.Table, manifest) {
		t.Errorf("OriginDispatch.RouteTable = %s %q, want the next table the build wrote", got.Format, got.Table)
	}
}

func TestAnEdgeThatRunsCodeTakesTheManifestTheOriginWouldHaveDispatchedBy(t *testing.T) {
	manifest := []byte(`{"routes":[]}`)
	root := servingRoot(t, "web", buildoutput.Hosting{RouteTable: edge.RouteTableNext, RootFunction: "index"}, manifest)
	query := servingQuery(root, "web", buildoutput.FrameworkNext)
	query.EdgeRunsCode = true

	facts, err := providerserver.AppServingFor(query)
	if err != nil {
		t.Fatalf("AppServingFor() = %v", err)
	}
	if facts.OriginDispatch != nil {
		t.Errorf("OriginDispatch = %+v where the edge runs the code, want the origin left out of dispatch", facts.OriginDispatch)
	}
	if facts.EdgeRouteTable == nil || !bytes.Equal(facts.EdgeRouteTable.Table, manifest) {
		t.Fatalf("EdgeRouteTable = %+v, want the route table the edge serves static assets and dispatches by", facts.EdgeRouteTable)
	}
	if want := query.Coordinate.RouteTableKey(""); !strings.HasPrefix(facts.EdgeRouteTable.Location.Key, strings.TrimSuffix(want, ".json")) {
		t.Errorf("EdgeRouteTable.Location.Key = %q, want a route-table key in the release's storage prefix", facts.EdgeRouteTable.Location.Key)
	}
}

func TestAContainerAppIsForwardedToAndNeverDispatchedByEitherSide(t *testing.T) {
	root := servingRoot(t, "web", buildoutput.Hosting{RouteTable: edge.RouteTableNext, RootFunction: "index"}, []byte(`{"routes":[]}`))
	for name, runsCode := range map[string]bool{"an edge that runs code": true, "an edge that runs none": false} {
		t.Run(name, func(t *testing.T) {
			query := servingQuery(root, "web", buildoutput.FrameworkNext)
			query.Compute = provider.ComputeContainer
			query.EdgeRunsCode = runsCode

			facts, err := providerserver.AppServingFor(query)
			if err != nil {
				t.Fatalf("AppServingFor() = %v", err)
			}
			if facts.OriginDispatch != nil || facts.EdgeRouteTable != nil {
				t.Errorf("OriginDispatch = %+v, EdgeRouteTable = %+v, want neither: a container is one service with no function to dispatch to", facts.OriginDispatch, facts.EdgeRouteTable)
			}
		})
	}
}

func TestAContainerAppIsNotRefusedForARoutingManifestItNeverUses(t *testing.T) {
	root := servingRoot(t, "web", buildoutput.Hosting{RouteTable: edge.RouteTableNext, RootFunction: "index"}, nil)
	query := servingQuery(root, "web", buildoutput.FrameworkNext)
	query.Compute = provider.ComputeContainer

	if _, err := providerserver.AppServingFor(query); err != nil {
		t.Fatalf("AppServingFor() = %v, want a container served without a route table", err)
	}
}

func TestAnEdgeThatRunsNoCodeHandsTheEdgeNothingToDispatchBy(t *testing.T) {
	root := servingRoot(t, "web", buildoutput.Hosting{RouteTable: edge.RouteTableNext, RootFunction: "index"}, []byte(`{}`))

	facts, err := providerserver.AppServingFor(servingQuery(root, "web", buildoutput.FrameworkNext))
	if err != nil {
		t.Fatalf("AppServingFor() = %v", err)
	}
	if facts.EdgeRouteTable != nil {
		t.Errorf("EdgeRouteTable = %+v where the origin dispatches, want the edge left out of dispatch", facts.EdgeRouteTable)
	}
}

func TestAnAppThatRoutesAtItsOriginAndWroteNoManifestIsRefused(t *testing.T) {
	root := servingRoot(t, "web", buildoutput.Hosting{RouteTable: edge.RouteTableNext, RootFunction: "index"}, nil)

	_, err := providerserver.AppServingFor(servingQuery(root, "web", buildoutput.FrameworkNext))
	if err == nil || !strings.Contains(err.Error(), edge.NextRouteTableFile) {
		t.Fatalf("AppServingFor() = %v, want a refusal naming %s", err, edge.NextRouteTableFile)
	}
}

func TestAnAppThatRoutesAtItsOriginAndNamesNoRootFunctionIsRefused(t *testing.T) {
	root := servingRoot(t, "web", buildoutput.Hosting{RouteTable: edge.RouteTableNext}, []byte(`{}`))

	_, err := providerserver.AppServingFor(servingQuery(root, "web", buildoutput.FrameworkNext))
	if err == nil || !strings.Contains(err.Error(), "root function") {
		t.Fatalf("AppServingFor() = %v, want a refusal naming the missing root function", err)
	}
}

func TestAnAppRoutingByARouteTableNoRouterReadsIsRefused(t *testing.T) {
	root := servingRoot(t, "web", buildoutput.Hosting{RouteTable: "astro", RootFunction: "index"}, []byte(`{}`))

	_, err := providerserver.AppServingFor(servingQuery(root, "web", buildoutput.FrameworkNode))
	if err == nil || !strings.Contains(err.Error(), `"astro"`) {
		t.Fatalf("AppServingFor() = %v, want a refusal naming the route table format no router reads", err)
	}
}

func builtRoutingApp(t *testing.T, app string, hosting buildoutput.Hosting, manifest []byte) {
	t.Helper()
	dir := filepath.Join(workingOutputRoot(t), "apps", app)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	hosting.Version = buildoutput.HostingVersion
	raw, err := json.Marshal(hosting)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, buildoutput.HostingFile), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if manifest != nil {
		if err := os.WriteFile(filepath.Join(dir, edge.NextRouteTableFile), manifest, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTheAppSpecIncludesEveryFactTheProvisionedAppServesFrom(t *testing.T) {
	builtProject(t)
	routing := []byte(`{"routes":[{"id":"index"}]}`)
	builtRoutingApp(t, "web", buildoutput.Hosting{RouteTable: edge.RouteTableNext, RootFunction: "index", FrameworkBuildID: "b1", ISR: true}, routing)

	vendor := fake.NewProvider(fake.Options{})
	client := servedBy(t, vendor)

	req := deployRequest()
	req.Edge = &contractv1.EdgeSelection{Kind: string(fake.KindDirect)}

	result, _ := deploy(t, client, req)
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	specs := vendor.FakeStacks().Provisioned()
	app := specs[len(specs)-1].App
	if app == nil {
		t.Fatal("the last spec the stacks port saw provisions no app")
	}
	if app.AssetPrefix == "" {
		t.Error("the app spec names no asset prefix, so the provisioned app serves its static files from nowhere")
	}
	if app.Bytecode == nil || app.Bytecode.Prefix == "" {
		t.Errorf("Bytecode = %+v, want the prefix the runtime warms its compile cache under", app.Bytecode)
	}
	if app.ISR == nil || app.ISR.Prefix == "" || app.ISR.TagNamespace == "" {
		t.Errorf("ISR = %+v, want the ledger a build declaring ISR revalidates through", app.ISR)
	}
	if app.Routing == nil || app.Routing.RootFunction != "index" || string(app.Routing.RouteTable.Table) != string(routing) {
		t.Errorf("Routing = %+v, want the entry route and manifest the build wrote", app.Routing)
	}
}

type recordingStacks struct {
	provider.Stacks

	mu    sync.Mutex
	drawn []provider.StackSpec
}

func (r *recordingStacks) Plan(ctx context.Context, spec provider.StackSpec, progress progress.Log) (provider.Plan, error) {
	r.mu.Lock()
	r.drawn = append(r.drawn, spec)
	r.mu.Unlock()
	return r.Stacks.Plan(ctx, spec, progress)
}

func (r *recordingStacks) drawnApps() []provider.StackSpec {
	r.mu.Lock()
	defer r.mu.Unlock()
	return appStacks(r.drawn)
}

func appStacks(specs []provider.StackSpec) []provider.StackSpec {
	var apps []provider.StackSpec
	for _, spec := range specs {
		if spec.App != nil {
			apps = append(apps, spec)
		}
	}
	return apps
}

type drawing struct {
	*fake.Provider

	releases *recordingStacks
}

func (d drawing) Stacks() provider.Stacks { return d.releases }

func drawingProvider() drawing {
	base := fake.NewProvider(fake.Options{})
	return drawing{base, &recordingStacks{Stacks: base.Stacks()}}
}

func TestADryDeployDrawsTheStackTheApplyWouldProvision(t *testing.T) {
	builtProject(t)
	vendor := drawingProvider()
	client := servedBy(t, vendor)

	req := deployRequest()
	req.Dry = true
	if result, _ := deploy(t, client, req); result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy(dry) = %q, want it to succeed", result.GetError())
	}

	drawn := vendor.releases.drawnApps()
	if len(drawn) != 1 {
		t.Fatalf("a dry deploy drew %d app stacks, want the one the manifest declares", len(drawn))
	}
	if result, _ := deploy(t, client, deployRequest()); result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}
	applied := appStacks(vendor.releases.Stacks.(*fake.Stacks).Provisioned())
	if len(applied) != 1 {
		t.Fatalf("the apply provisioned %d app stacks, want the one the manifest declares", len(applied))
	}
	if drawn[0].App.App != applied[0].App.App {
		t.Errorf("the plan was drawn of %s but the apply provisions %s, want one stack",
			drawn[0].App.App, applied[0].App.App)
	}
}

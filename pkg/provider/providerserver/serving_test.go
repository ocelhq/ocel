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
	raw, err := json.Marshal(hosting)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, buildoutput.HostingFile), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if manifest != nil {
		if err := os.WriteFile(filepath.Join(dir, edge.RoutingManifestFile), manifest, 0o644); err != nil {
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

func TestOnlyNextAsksForAnISRLedger(t *testing.T) {
	next, err := providerserver.AppServingFor(servingQuery(t.TempDir(), "web", buildoutput.FrameworkNext))
	if err != nil {
		t.Fatalf("AppServingFor() = %v", err)
	}
	if next.ISR == nil || next.ISR.Prefix == "" || next.ISR.TagNamespace == "" {
		t.Fatalf("ISR = %+v, want the prefix and tag namespace a revalidation writes through", next.ISR)
	}
	other, err := providerserver.AppServingFor(servingQuery(t.TempDir(), "web", "astro"))
	if err != nil {
		t.Fatalf("AppServingFor() = %v", err)
	}
	if other.ISR != nil {
		t.Errorf("ISR = %+v for a framework that revalidates nothing, want none", other.ISR)
	}
}

func TestAnAppDispatchingAtItsOriginIncludesTheManifestItDispatchesBy(t *testing.T) {
	manifest := []byte(`{"routes":[]}`)
	root := servingRoot(t, "web", buildoutput.Hosting{EdgeRouting: true, Entry: "index"}, manifest)

	facts, err := providerserver.AppServingFor(servingQuery(root, "web", buildoutput.FrameworkNext))
	if err != nil {
		t.Fatalf("AppServingFor() = %v", err)
	}
	if facts.OriginDispatch == nil {
		t.Fatal("OriginDispatch = nil for an app whose build says it dispatches at its origin")
	}
	if facts.OriginDispatch.Entry != "index" {
		t.Errorf("OriginDispatch.Entry = %q, want the entry route the build named", facts.OriginDispatch.Entry)
	}
	if !bytes.Equal(facts.OriginDispatch.Manifest, manifest) {
		t.Errorf("OriginDispatch.Manifest = %q, want the bytes the build wrote", facts.OriginDispatch.Manifest)
	}
}

func TestAnEdgeThatRunsCodeTakesTheManifestTheOriginWouldHaveDispatchedBy(t *testing.T) {
	manifest := []byte(`{"routes":[]}`)
	root := servingRoot(t, "web", buildoutput.Hosting{EdgeRouting: true, Entry: "index"}, manifest)
	query := servingQuery(root, "web", buildoutput.FrameworkNext)
	query.EdgeRunsCode = true

	facts, err := providerserver.AppServingFor(query)
	if err != nil {
		t.Fatalf("AppServingFor() = %v", err)
	}
	if facts.OriginDispatch != nil {
		t.Errorf("OriginDispatch = %+v where the edge runs the code, want the origin left out of dispatch", facts.OriginDispatch)
	}
	if facts.EdgeDispatch == nil || !bytes.Equal(facts.EdgeDispatch.Manifest, manifest) {
		t.Fatalf("EdgeDispatch = %+v, want the manifest the edge serves static assets and dispatches by", facts.EdgeDispatch)
	}
}

func TestAContainerAppIsForwardedToAndNeverDispatchedByEitherSide(t *testing.T) {
	root := servingRoot(t, "web", buildoutput.Hosting{EdgeRouting: true, Entry: "index"}, []byte(`{"routes":[]}`))
	for name, runsCode := range map[string]bool{"an edge that runs code": true, "an edge that runs none": false} {
		t.Run(name, func(t *testing.T) {
			query := servingQuery(root, "web", buildoutput.FrameworkNext)
			query.Compute = provider.ComputeContainer
			query.EdgeRunsCode = runsCode

			facts, err := providerserver.AppServingFor(query)
			if err != nil {
				t.Fatalf("AppServingFor() = %v", err)
			}
			if facts.OriginDispatch != nil || facts.EdgeDispatch != nil {
				t.Errorf("OriginDispatch = %+v, EdgeDispatch = %+v, want neither: a container is one service with no function to dispatch to", facts.OriginDispatch, facts.EdgeDispatch)
			}
		})
	}
}

func TestAContainerAppIsNotRefusedForARoutingManifestItNeverUses(t *testing.T) {
	root := servingRoot(t, "web", buildoutput.Hosting{EdgeRouting: true, Entry: "index"}, nil)
	query := servingQuery(root, "web", buildoutput.FrameworkNext)
	query.Compute = provider.ComputeContainer

	if _, err := providerserver.AppServingFor(query); err != nil {
		t.Fatalf("AppServingFor() = %v, want a container served without a routing manifest", err)
	}
}

func TestAnEdgeThatRunsNoCodeHandsTheEdgeNothingToDispatchBy(t *testing.T) {
	root := servingRoot(t, "web", buildoutput.Hosting{EdgeRouting: true, Entry: "index"}, []byte(`{}`))

	facts, err := providerserver.AppServingFor(servingQuery(root, "web", buildoutput.FrameworkNext))
	if err != nil {
		t.Fatalf("AppServingFor() = %v", err)
	}
	if facts.EdgeDispatch != nil {
		t.Errorf("EdgeDispatch = %+v where the origin dispatches, want the edge left out of dispatch", facts.EdgeDispatch)
	}
}

func TestAnAppThatRoutesAtItsOriginAndWroteNoManifestIsRefused(t *testing.T) {
	root := servingRoot(t, "web", buildoutput.Hosting{EdgeRouting: true, Entry: "index"}, nil)

	_, err := providerserver.AppServingFor(servingQuery(root, "web", buildoutput.FrameworkNext))
	if err == nil || !strings.Contains(err.Error(), edge.RoutingManifestFile) {
		t.Fatalf("AppServingFor() = %v, want a refusal naming %s", err, edge.RoutingManifestFile)
	}
}

func TestAnAppThatRoutesAtItsOriginAndNamesNoEntryIsRefused(t *testing.T) {
	root := servingRoot(t, "web", buildoutput.Hosting{EdgeRouting: true}, []byte(`{}`))

	_, err := providerserver.AppServingFor(servingQuery(root, "web", buildoutput.FrameworkNext))
	if err == nil || !strings.Contains(err.Error(), "entry route") {
		t.Fatalf("AppServingFor() = %v, want a refusal naming the missing entry route", err)
	}
}

func builtRoutingApp(t *testing.T, app string, hosting buildoutput.Hosting, manifest []byte) {
	t.Helper()
	dir := filepath.Join(workingOutputRoot(t), "apps", app)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(hosting)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, buildoutput.HostingFile), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if manifest != nil {
		if err := os.WriteFile(filepath.Join(dir, edge.RoutingManifestFile), manifest, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTheAppSpecIncludesEveryFactTheProvisionedAppServesFrom(t *testing.T) {
	builtProject(t)
	routing := []byte(`{"routes":[{"id":"index"}]}`)
	builtRoutingApp(t, "web", buildoutput.Hosting{EdgeRouting: true, Entry: "index", FrameworkBuildID: "b1"}, routing)

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
		t.Errorf("ISR = %+v, want the ledger a next app revalidates through", app.ISR)
	}
	if app.Routing == nil || app.Routing.Entry != "index" || string(app.Routing.Manifest) != string(routing) {
		t.Errorf("Routing = %+v, want the entry route and manifest the build wrote", app.Routing)
	}
}

func TestTheStagedRecordIncludesTheManifestAnEdgeRunningCodeRoutesBy(t *testing.T) {
	builtProject(t)
	routing := []byte(`{"routes":[{"id":"index"}]}`)
	builtRoutingApp(t, "web", buildoutput.Hosting{EdgeRouting: true, Entry: "index", FrameworkBuildID: "b1"}, routing)
	client, vendor := deployServed(t)
	stager := staging(t, vendor)

	req := deployRequest()
	req.Edge = &contractv1.EdgeSelection{Kind: string(fake.KindRelay)}

	result, _ := deploy(t, client, req)
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	staged := stager.records()
	if len(staged) != 1 {
		t.Fatalf("the deploy staged %d records, want the one app it released", len(staged))
	}
	encoded, err := json.Marshal(staged[0])
	if err != nil {
		t.Fatal(err)
	}
	var record struct {
		RoutingManifest json.RawMessage `json:"routingManifest"`
	}
	if err := json.Unmarshal(encoded, &record); err != nil {
		t.Fatal(err)
	}
	if string(record.RoutingManifest) != string(routing) {
		t.Errorf("the staged record routes by %s, want %s: an edge that runs the code reads its routing from the record, and without it proxies every static asset to the origin", record.RoutingManifest, routing)
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

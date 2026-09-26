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

	"github.com/ocelhq/ocel/pkg/naming"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/providerkit/appbuild"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/providerserver"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func servingRoot(t *testing.T, app string, desc edge.ServeDescriptor, manifest []byte) string {
	t.Helper()
	root := t.TempDir()
	dir := appbuild.AppArtifactRoot(root, app)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(desc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, edge.ServeDescriptorFile), raw, 0o644); err != nil {
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
		Stack:      naming.AppStack("production", app, naming.NewRelease("dep1", "fp1")),
		Coordinate: naming.Coordinate{Project: "shop", Env: "production", App: app, Release: naming.NewRelease("dep1", "fp1")},
	}
}

func TestEveryAppCarriesTheAssetPrefixAndBytecodeCacheItServesFrom(t *testing.T) {
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
	next, err := providerserver.AppServingFor(servingQuery(t.TempDir(), "web", appbuild.FrameworkNext))
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

func TestAnAppRoutingAtItsOriginCarriesTheManifestItRoutesBy(t *testing.T) {
	manifest := []byte(`{"routes":[]}`)
	root := servingRoot(t, "web", edge.ServeDescriptor{EdgeRouting: true, Entry: "index"}, manifest)

	facts, err := providerserver.AppServingFor(servingQuery(root, "web", appbuild.FrameworkNext))
	if err != nil {
		t.Fatalf("AppServingFor() = %v", err)
	}
	if facts.Routing == nil {
		t.Fatal("Routing = nil for an app whose build says it routes at its origin")
	}
	if facts.Routing.Entry != "index" {
		t.Errorf("Routing.Entry = %q, want the entry route the build named", facts.Routing.Entry)
	}
	if !bytes.Equal(facts.Routing.Manifest, manifest) {
		t.Errorf("Routing.Manifest = %q, want the bytes the build wrote", facts.Routing.Manifest)
	}
}

func TestAnEdgeThatRunsCodeTakesTheManifestTheOriginWouldHaveRoutedBy(t *testing.T) {
	manifest := []byte(`{"routes":[]}`)
	root := servingRoot(t, "web", edge.ServeDescriptor{EdgeRouting: true, Entry: "index"}, manifest)
	query := servingQuery(root, "web", appbuild.FrameworkNext)
	query.EdgeRunsCode = true

	facts, err := providerserver.AppServingFor(query)
	if err != nil {
		t.Fatalf("AppServingFor() = %v", err)
	}
	if facts.Routing != nil {
		t.Errorf("Routing = %+v where the edge runs the code, want the origin left out of routing", facts.Routing)
	}
	if facts.EdgeRouting == nil || !bytes.Equal(facts.EdgeRouting.Manifest, manifest) {
		t.Fatalf("EdgeRouting = %+v, want the manifest the edge serves static assets and routes by", facts.EdgeRouting)
	}
}

func TestAnEdgeThatRunsNoCodeHandsTheEdgeNothingToRouteBy(t *testing.T) {
	root := servingRoot(t, "web", edge.ServeDescriptor{EdgeRouting: true, Entry: "index"}, []byte(`{}`))

	facts, err := providerserver.AppServingFor(servingQuery(root, "web", appbuild.FrameworkNext))
	if err != nil {
		t.Fatalf("AppServingFor() = %v", err)
	}
	if facts.EdgeRouting != nil {
		t.Errorf("EdgeRouting = %+v where the origin routes, want the edge left out of routing", facts.EdgeRouting)
	}
}

func TestAnAppThatRoutesAtItsOriginAndWroteNoManifestIsRefused(t *testing.T) {
	root := servingRoot(t, "web", edge.ServeDescriptor{EdgeRouting: true, Entry: "index"}, nil)

	_, err := providerserver.AppServingFor(servingQuery(root, "web", appbuild.FrameworkNext))
	if err == nil || !strings.Contains(err.Error(), edge.RoutingManifestFile) {
		t.Fatalf("AppServingFor() = %v, want a refusal naming %s", err, edge.RoutingManifestFile)
	}
}

func TestAnAppThatRoutesAtItsOriginAndNamesNoEntryIsRefused(t *testing.T) {
	root := servingRoot(t, "web", edge.ServeDescriptor{EdgeRouting: true}, []byte(`{}`))

	_, err := providerserver.AppServingFor(servingQuery(root, "web", appbuild.FrameworkNext))
	if err == nil || !strings.Contains(err.Error(), "entry route") {
		t.Fatalf("AppServingFor() = %v, want a refusal naming the missing entry route", err)
	}
}

func builtRoutingApp(t *testing.T, app string, desc edge.ServeDescriptor, manifest []byte) {
	t.Helper()
	dir := filepath.Join(appbuild.ArtifactRoot(), "apps", app)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(desc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, edge.ServeDescriptorFile), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if manifest != nil {
		if err := os.WriteFile(filepath.Join(dir, edge.RoutingManifestFile), manifest, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTheAppSpecCarriesEveryFactTheStoodUpAppServesFrom(t *testing.T) {
	builtProject(t)
	routing := []byte(`{"routes":[{"id":"index"}]}`)
	builtRoutingApp(t, "web", edge.ServeDescriptor{EdgeRouting: true, Entry: "index", BuildID: "b1"}, routing)

	provider := fake.NewProvider(fake.Options{})
	client := servedBy(t, provider)

	req := deployRequest()
	req.Edge = &contractv1.EdgeSelection{Kind: string(fake.KindDirect)}

	result, _ := deploy(t, client, req)
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	specs := provider.FakeStacks().Provisioned()
	app := specs[len(specs)-1].App
	if app == nil {
		t.Fatal("the last spec the stacks port saw stands up no app")
	}
	if app.AssetPrefix == "" {
		t.Error("the app spec names no asset prefix, so the stood-up app serves its static files from nowhere")
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

func TestTheStagedRecordCarriesTheManifestAnEdgeRunningCodeRoutesBy(t *testing.T) {
	builtProject(t)
	routing := []byte(`{"routes":[{"id":"index"}]}`)
	builtRoutingApp(t, "web", edge.ServeDescriptor{EdgeRouting: true, Entry: "index", BuildID: "b1"}, routing)
	client, provider := deployServed(t)
	held := staging(t, provider)

	req := deployRequest()
	req.Edge = &contractv1.EdgeSelection{Kind: string(fake.KindRelay)}

	result, _ := deploy(t, client, req)
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	staged := held.records()
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

func (r *recordingStacks) Plan(ctx context.Context, spec provider.StackSpec, progress edge.Progress) (provider.Plan, error) {
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
	provider := drawingProvider()
	client := servedBy(t, provider)

	req := deployRequest()
	req.Dry = true
	if result, _ := deploy(t, client, req); result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy(dry) = %q, want it to succeed", result.GetError())
	}

	drawn := provider.releases.drawnApps()
	if len(drawn) != 1 {
		t.Fatalf("a dry deploy drew %d app stacks, want the one the manifest declares", len(drawn))
	}
	if result, _ := deploy(t, client, deployRequest()); result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}
	applied := appStacks(provider.releases.Stacks.(*fake.Stacks).Provisioned())
	if len(applied) != 1 {
		t.Fatalf("the apply provisioned %d app stacks, want the one the manifest declares", len(applied))
	}
	if drawn[0].App.App != applied[0].App.App {
		t.Errorf("the plan was drawn of %s but the apply provisions %s, want one stack",
			drawn[0].App.App, applied[0].App.App)
	}
}

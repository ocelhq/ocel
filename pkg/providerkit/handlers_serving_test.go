package providerkit_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func builtRoutingApp(t *testing.T, app string, desc edge.ServeDescriptor, manifest []byte) {
	t.Helper()
	dir := filepath.Join(providerkit.ArtifactRoot(), "apps", app)
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

func TestTheAppPlanCarriesEveryFactTheStoodUpAppServesFrom(t *testing.T) {
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

	plans := provider.Releases().(*fake.Releaser).Plans()
	app := plans[len(plans)-1].App
	if app == nil {
		t.Fatal("the last plan the releaser saw stands up no app")
	}
	if app.AssetPrefix == "" {
		t.Error("the app plan names no asset prefix, so the stood-up app serves its static files from nowhere")
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

type recordingReleaser struct {
	providerkit.Releaser

	mu    sync.Mutex
	drawn []providerkit.StackPlan
}

func (r *recordingReleaser) Plan(ctx context.Context, plan providerkit.StackPlan, report providerkit.Reporter) (providerkit.Plan, error) {
	r.mu.Lock()
	r.drawn = append(r.drawn, plan)
	r.mu.Unlock()
	return r.Releaser.Plan(ctx, plan, report)
}

func (r *recordingReleaser) drawnApps() []providerkit.StackPlan {
	r.mu.Lock()
	defer r.mu.Unlock()
	return appStacks(r.drawn)
}

func appStacks(plans []providerkit.StackPlan) []providerkit.StackPlan {
	var apps []providerkit.StackPlan
	for _, plan := range plans {
		if plan.App != nil {
			apps = append(apps, plan)
		}
	}
	return apps
}

type drawing struct {
	*fake.Provider

	releases *recordingReleaser
}

func (d drawing) Releases() providerkit.Releaser { return d.releases }

func drawingProvider() drawing {
	base := fake.NewProvider(fake.Options{})
	return drawing{base, &recordingReleaser{Releaser: base.Releases()}}
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
	applied := appStacks(provider.releases.Releaser.(*fake.Releaser).Plans())
	if len(applied) != 1 {
		t.Fatalf("the apply provisioned %d app stacks, want the one the manifest declares", len(applied))
	}
	if drawn[0].App.App != applied[0].App.App {
		t.Errorf("the plan was drawn of %s but the apply provisions %s, want one stack",
			drawn[0].App.App, applied[0].App.App)
	}
}

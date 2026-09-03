package providerkit_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type carrying struct{ *fake.Provider }

func (carrying) Membrane(context.Context) ([]byte, error) { return []byte(fake.Membrane), nil }

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

	provider := carrying{fake.NewProvider(fake.Options{})}
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
	if app.Membrane.Key == "" {
		t.Error("the app plan names no membrane, so the functions have nothing to boot through")
	}
	if !strings.HasPrefix(app.Membrane.Key, providerkit.MembranePrefix) {
		t.Errorf("Membrane.Key = %q, want it under %q", app.Membrane.Key, providerkit.MembranePrefix)
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

func TestAProviderCarryingNoMembraneStandsAnAppUpWithout(t *testing.T) {
	builtProject(t)
	provider := fake.NewProvider(fake.Options{})
	client := servedBy(t, provider)

	result, _ := deploy(t, client, deployRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want a provider whose functions boot on their own to deploy all the same", result.GetError())
	}
	plans := provider.Releases().(*fake.Releaser).Plans()
	if ref := plans[len(plans)-1].App.Membrane; ref != (providerkit.ArtifactRef{}) {
		t.Errorf("Membrane = %+v where the provider carries none, want nothing placed", ref)
	}
}

func placedMembranes(t *testing.T, store providerkit.ArtifactStore) int {
	t.Helper()
	objects, holds := store.(*fake.Artifacts)
	if !holds {
		t.Fatalf("Artifacts() = %T, want the store the test reads placements back from", store)
	}
	placed := 0
	for _, ref := range objects.Keys() {
		if strings.HasPrefix(ref.Key, providerkit.MembranePrefix) {
			placed++
		}
	}
	return placed
}

func TestADryDeployPlacesNoMembrane(t *testing.T) {
	builtProject(t)
	provider := carrying{fake.NewProvider(fake.Options{})}
	client := servedBy(t, provider)

	req := deployRequest()
	req.Dry = true

	result, _ := deploy(t, client, req)
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}
	if placed := placedMembranes(t, provider.Artifacts()); placed != 0 {
		t.Errorf("a dry deploy placed %d membranes, want a run that asks to write nothing", placed)
	}
}

func TestADeployThatIsNotDryPlacesTheMembrane(t *testing.T) {
	builtProject(t)
	provider := carrying{fake.NewProvider(fake.Options{})}
	client := servedBy(t, provider)

	result, _ := deploy(t, client, deployRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}
	if placed := placedMembranes(t, provider.Artifacts()); placed != 1 {
		t.Errorf("an applying deploy placed %d membranes, want the one its functions boot through", placed)
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
	carrying

	releases *recordingReleaser
}

func (d drawing) Releases() providerkit.Releaser { return d.releases }

func drawingProvider() drawing {
	base := fake.NewProvider(fake.Options{})
	return drawing{carrying{base}, &recordingReleaser{Releaser: base.Releases()}}
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
	if drawn[0].App.Membrane == (providerkit.ArtifactRef{}) {
		t.Fatal("the drawn app stack carries no membrane, so the plan is drawn of a stack the apply would never provision")
	}

	if result, _ := deploy(t, client, deployRequest()); result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}
	applied := appStacks(provider.releases.Releaser.(*fake.Releaser).Plans())
	if len(applied) != 1 {
		t.Fatalf("the apply provisioned %d app stacks, want the one the manifest declares", len(applied))
	}
	if drawn[0].App.Membrane != applied[0].App.Membrane {
		t.Errorf("the plan was drawn of a stack booting through %+v but the apply provisions %+v, want one stack",
			drawn[0].App.Membrane, applied[0].App.Membrane)
	}
}

func membraneRow(t *testing.T, plan providerkit.Plan) providerkit.ChangeGroup {
	t.Helper()
	for _, group := range plan.Groups {
		if group.Kind == providerkit.UploadKind && group.Name == "membrane" {
			return group
		}
	}
	t.Fatalf("the plan shows %v with no membrane row, want the upload the apply would make", plan.Groups)
	return providerkit.ChangeGroup{}
}

func TestADryDeployPlansTheMembraneUploadItDoesNotMake(t *testing.T) {
	builtProject(t)
	provider := carrying{fake.NewProvider(fake.Options{})}
	client := servedBy(t, provider)

	req := deployRequest()
	req.Dry = true
	result, events := deploy(t, client, req)
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy(dry) = %q, want it to succeed", result.GetError())
	}
	drawn, err := providerkit.PlanOf(lastPlan(events))
	if err != nil {
		t.Fatal(err)
	}
	if action := membraneRow(t, drawn).Action; action != providerkit.ActionCreate {
		t.Errorf("the membrane row reads %q against an empty store, want %q", action, providerkit.ActionCreate)
	}
	if placed := placedMembranes(t, provider.Artifacts()); placed != 0 {
		t.Errorf("planning the membrane placed %d of them, want a row derived by reading alone", placed)
	}

	if _, err := providerkit.PlaceMembrane(context.Background(), provider, providerkit.ClassProduction, provider.Artifacts(), nil); err != nil {
		t.Fatal(err)
	}

	result, events = deploy(t, client, req)
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy(dry) = %q, want it to succeed", result.GetError())
	}
	if drawn, err = providerkit.PlanOf(lastPlan(events)); err != nil {
		t.Fatal(err)
	}
	if action := membraneRow(t, drawn).Action; action != providerkit.ActionKeep {
		t.Errorf("the membrane row reads %q once the membrane stands, want %q", action, providerkit.ActionKeep)
	}
}

package providerkit_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func builtEdgeBundle(t *testing.T, app string, bundle []byte) {
	t.Helper()
	path := filepath.Join(providerkit.AppArtifactRoot(providerkit.ArtifactRoot(), app), filepath.FromSlash(edge.AppBundleFile))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, bundle, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestTheStagedRecordKeysFunctionURLsByTheRouteTheManifestNames(t *testing.T) {
	builtProject(t)
	client, provider := deployServed(t)
	held := staging(t, provider)

	req := deployRequest()
	req.Edge = &contractv1.EdgeSelection{Kind: string(fake.KindRelay)}
	req.Manifest.Functions[0].RouteId = "bundle-0"

	result, _ := deploy(t, client, req)
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	staged := held.records()
	if len(staged) != 1 {
		t.Fatalf("the deploy staged %d records, want the one app it released", len(staged))
	}
	urls := staged[0].FunctionURLs
	stood := result.GetFunctions()
	if len(stood) != 1 {
		t.Fatalf("the deploy stood up %d functions, want the one the manifest declares", len(stood))
	}
	if urls["bundle-0"] != stood[0].GetUrl() {
		t.Errorf("functionUrls[bundle-0] = %q, want the URL %q the function stands on: the router reaches a target by the route the manifest names, and a record keyed by logical name answers every page 502",
			urls["bundle-0"], stood[0].GetUrl())
	}
	if _, keyed := urls["server"]; keyed {
		t.Errorf("functionUrls = %v, want no entry under the logical name", urls)
	}
}

func TestTheStagedRecordNamesTheEntryTheBuildRoutesThrough(t *testing.T) {
	builtProject(t)
	builtRoutingApp(t, "web", edge.ServeDescriptor{Entry: "/"}, nil)
	client, provider := deployServed(t)
	held := staging(t, provider)

	req := deployRequest()
	req.Edge = &contractv1.EdgeSelection{Kind: string(fake.KindRelay)}
	req.Manifest.Functions[0].RouteId = "/"

	result, _ := deploy(t, client, req)
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	staged := held.records()
	if len(staged) != 1 {
		t.Fatalf("the deploy staged %d records, want the one app it released", len(staged))
	}
	record := staged[0]
	if record.Entry != "/" {
		t.Errorf("entry = %q, want the route the build serves through: an edge that fronts a release reaches it at functionUrls[entry]", record.Entry)
	}
	if record.FunctionURLs[record.Entry] == "" {
		t.Errorf("functionUrls[%q] = \"\", want the entry to name a URL the edge can reach", record.Entry)
	}
	if record.EntryFunction == "" {
		t.Error("the staged record names no entry function, so an edge that invokes the release by name has nothing to call")
	}
}

func TestTheStagedRecordCarriesTheCodeAndVariablesAnEdgeRunsTheAppWith(t *testing.T) {
	builtProject(t)
	bundle := []byte(`{"version":1}`)
	builtEdgeBundle(t, "web", bundle)
	client, provider := deployServed(t)
	held := staging(t, provider)

	req := deployRequest()
	req.Edge = &contractv1.EdgeSelection{Kind: string(fake.KindRelay)}
	req.Manifest.Apps[0].Folder = "/web"
	req.Manifest.Apps[0].Variables = []*contractv1.ManifestVariable{{
		Key:   "PUBLIC_MODE",
		Value: "loud",
		Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN,
	}}

	result, _ := deploy(t, client, req)
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	staged := held.records()
	if len(staged) != 1 {
		t.Fatalf("the deploy staged %d records, want the one app it released", len(staged))
	}
	record := staged[0]
	if record.EdgeWorkers == nil {
		t.Fatal("the staged record carries no edgeWorkers, so an edge that runs the app's code has nothing to load")
	}
	if record.EdgeWorkers.BundleKey == "" {
		t.Error("edgeWorkers names no bundle key, so the edge cannot fetch the bundle the release uploaded")
	}
	if len(record.EdgeWorkers.ID) != 64 {
		t.Errorf("edgeWorkers.id = %q, want the sha256 of the bundle and the runtime it loads under", record.EdgeWorkers.ID)
	}
	if record.EdgeWorkers.ID != providerkit.LoaderID(bundle, fake.CompatDate, []string{fake.CompatFlag}) {
		t.Errorf("edgeWorkers.id = %q, want the id derived from the bundle on disk and the edge's own runtime", record.EdgeWorkers.ID)
	}
	if record.EdgeWorkers.CompatDate != fake.CompatDate || !slices.Equal(record.EdgeWorkers.CompatFlags, []string{fake.CompatFlag}) {
		t.Errorf("edgeWorkers runtime = %q %v, want the one the edge names", record.EdgeWorkers.CompatDate, record.EdgeWorkers.CompatFlags)
	}
	if record.Env["PUBLIC_MODE"] != "loud" {
		t.Errorf("env = %v, want the app's plain variables, which the edge passes into the worker", record.Env)
	}
	if record.Env["OCEL_APP_FOLDER"] != "/web" {
		t.Errorf("env[OCEL_APP_FOLDER] = %q, want the folder the app is rooted at", record.Env["OCEL_APP_FOLDER"])
	}
	if _, named := record.Env["orders"]; named {
		t.Errorf("env = %v, want no link name among the variables the worker runs with", record.Env)
	}
	if record.IsrWriteSecret == "" {
		t.Error("the staged record carries no isrWriteSecret, so the edge cannot write a revalidated page back")
	}
}

func TestTheStagedRecordNamesTheISRPrefixTheFunctionWritesUnder(t *testing.T) {
	builtProject(t)
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
	plans := provider.Releases().(*fake.Releaser).Plans()
	app := plans[len(plans)-1].App
	if app == nil || app.ISR == nil {
		t.Fatal("the last plan the releaser saw carries no ISR plan")
	}
	if staged[0].IsrPrefix != app.ISR.Prefix {
		t.Errorf("isrPrefix = %q, want %q: the edge reads entries at <isrPrefix>/cache/<route>.cache.json, so a prefix that differs from the one the function writes under misses every prerender",
			staged[0].IsrPrefix, app.ISR.Prefix)
	}
	if strings.HasSuffix(staged[0].IsrPrefix, "/") {
		t.Errorf("isrPrefix = %q, want no trailing slash", staged[0].IsrPrefix)
	}
}

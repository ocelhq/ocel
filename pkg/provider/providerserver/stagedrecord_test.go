package providerserver_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/processenv"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/providerserver"
)

func builtEdgeBundle(t *testing.T, app string, bundle []byte) {
	t.Helper()
	path := filepath.Join(buildoutput.AppRoot(workingOutputRoot(t), app), filepath.FromSlash(edge.AppBundleFile))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, bundle, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestTheStagedRecordKeysFunctionURLsByTheRouteTheManifestNames(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	stager := staging(t, vendor)

	req := deployRequest()
	req.Edge = &contractv1.EdgeSelection{Kind: string(fake.KindRelay)}
	webFunctions(req).Functions[0].RouteId = "bundle-0"

	result, _ := deploy(t, client, req)
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	staged := stager.records()
	if len(staged) != 1 {
		t.Fatalf("the deploy staged %d records, want the one app it released", len(staged))
	}
	urls := staged[0].FunctionURLs
	var functions []provider.Function
	for _, spec := range vendor.FakeStacks().Provisioned() {
		functions = append(functions, fake.ProvisionedFunctions(spec)...)
	}
	if len(functions) != 1 {
		t.Fatalf("the deploy provisioned %d functions, want the one the manifest declares", len(functions))
	}
	if urls["bundle-0"] != functions[0].URL {
		t.Errorf("functionUrls[bundle-0] = %q, want the URL %q the function is reachable at: dispatch reaches a target by the route the manifest names, and a record keyed by logical name answers every page 502",
			urls["bundle-0"], functions[0].URL)
	}
	if _, keyed := urls["server"]; keyed {
		t.Errorf("functionUrls = %v, want no entry under the logical name", urls)
	}
}

func TestTheStagedRecordNamesTheEntryTheBuildRoutesThrough(t *testing.T) {
	builtProject(t)
	builtRoutingApp(t, "web", buildoutput.Hosting{RootFunction: "/"}, nil)
	client, provider := deployServed(t)
	stager := staging(t, provider)

	req := deployRequest()
	req.Edge = &contractv1.EdgeSelection{Kind: string(fake.KindRelay)}
	webFunctions(req).Functions[0].RouteId = "/"

	result, _ := deploy(t, client, req)
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	staged := stager.records()
	if len(staged) != 1 {
		t.Fatalf("the deploy staged %d records, want the one app it released", len(staged))
	}
	record := staged[0]
	if record.RootFunction != "/" {
		t.Errorf("rootFunction = %q, want the route the build serves through: an edge that fronts a release reaches it at functionUrls[rootFunction]", record.RootFunction)
	}
	if record.FunctionURLs[record.RootFunction] == "" {
		t.Errorf("functionUrls[%q] = \"\", want the root function to name a URL the edge can reach", record.RootFunction)
	}
	if record.RootFunctionPhysical == "" {
		t.Error("the staged record names no root function, so an edge that invokes the release by name has nothing to call")
	}
}

func TestTheStagedRecordIncludesTheCodeAndVariablesAnEdgeRunsTheAppWith(t *testing.T) {
	builtProject(t)
	bundle := []byte(`{"version":1}`)
	builtEdgeBundle(t, "web", bundle)
	client, provider := deployServed(t)
	stager := staging(t, provider)

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

	staged := stager.records()
	if len(staged) != 1 {
		t.Fatalf("the deploy staged %d records, want the one app it released", len(staged))
	}
	record := staged[0]
	if record.EdgeWorkers == nil {
		t.Fatal("the staged record has no edgeWorkers, so an edge that runs the app's code has nothing to load")
	}
	if record.EdgeWorkers.BundleKey == "" {
		t.Error("edgeWorkers names no bundle key, so the edge cannot fetch the bundle the release uploaded")
	}
	if len(record.EdgeWorkers.ID) != 64 {
		t.Errorf("edgeWorkers.id = %q, want the sha256 of the bundle and the runtime it loads under", record.EdgeWorkers.ID)
	}
	if record.EdgeWorkers.ID != providerserver.LoaderID(bundle, fake.CompatDate, []string{fake.CompatFlag}) {
		t.Errorf("edgeWorkers.id = %q, want the id derived from the bundle on disk and the edge's own compatibility", record.EdgeWorkers.ID)
	}
	if record.EdgeWorkers.CompatDate != fake.CompatDate || !slices.Equal(record.EdgeWorkers.CompatFlags, []string{fake.CompatFlag}) {
		t.Errorf("edgeWorkers compatibility = %q %v, want the one the edge names", record.EdgeWorkers.CompatDate, record.EdgeWorkers.CompatFlags)
	}
	if record.Env["PUBLIC_MODE"] != "loud" {
		t.Errorf("env = %v, want the app's plain variables, which the edge passes into the worker", record.Env)
	}
	if record.Env[processenv.AppFolderEnvVar] != "/web" {
		t.Errorf("env[%s] = %q, want the folder the app is rooted at", processenv.AppFolderEnvVar, record.Env[processenv.AppFolderEnvVar])
	}
	if _, named := record.Env["orders"]; named {
		t.Errorf("env = %v, want no binding name among the variables the worker runs with", record.Env)
	}
	if record.IsrWriteSecret == "" {
		t.Error("the staged record has no isrWriteSecret, so the edge cannot write a revalidated page back")
	}
}

func TestTheStagedRecordNamesTheISRPrefixTheFunctionWritesUnder(t *testing.T) {
	builtProject(t)
	client, provider := deployServed(t)
	stager := staging(t, provider)

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
	specs := provider.FakeStacks().Provisioned()
	app := specs[len(specs)-1].App
	if app == nil || app.ISR == nil {
		t.Fatal("the last spec the stacks port saw has no ISR spec")
	}
	if staged[0].IsrPrefix != app.ISR.Prefix {
		t.Errorf("isrPrefix = %q, want %q: the edge reads entries at <isrPrefix>/cache/<route>.cache.json, so a prefix that differs from the one the function writes under misses every prerender",
			staged[0].IsrPrefix, app.ISR.Prefix)
	}
	if strings.HasSuffix(staged[0].IsrPrefix, "/") {
		t.Errorf("isrPrefix = %q, want no trailing slash", staged[0].IsrPrefix)
	}
}

func TestTheStagedRecordIncludesNoCodeForAnEdgeThatRunsNone(t *testing.T) {
	builtProject(t)
	builtEdgeBundle(t, "web", []byte(`{"version":1}`))
	client, provider := deployServed(t)
	direct := provider.Edges().(*fake.Edges).Edge(fake.KindDirect)
	stager := staging(t, provider)
	if !direct.Facts().Compatibility.IsZero() {
		t.Fatal("the reference direct edge names a compatibility, so it cannot represent an edge that runs no code")
	}

	req := deployRequest()
	req.Edge = &contractv1.EdgeSelection{Kind: string(fake.KindDirect)}
	result, _ := deploy(t, client, req)
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	staged := stager.records()
	if len(staged) != 1 {
		t.Fatalf("the deploy staged %d records, want the one app it released", len(staged))
	}
	if staged[0].EdgeWorkers != nil {
		t.Errorf("edgeWorkers = %+v, want none: the edge runs no code, so nothing loads the bundle the build left", staged[0].EdgeWorkers)
	}
}

func TestAServerlessAppBehindAnEdgeThatRunsCodeRecordsItsEntryFunctionAsOrigin(t *testing.T) {
	builtProject(t)
	builtRoutingApp(t, "web", buildoutput.Hosting{RouteTable: edge.RouteTableNext, RootFunction: "bundle-0", FrameworkBuildID: "b1"}, []byte(`{"routes":[{"id":"bundle-0"}]}`))
	client, vendor := deployServed(t)
	stager := staging(t, vendor)

	req := deployRequest()
	req.Edge = &contractv1.EdgeSelection{Kind: string(fake.KindRelay)}
	webFunctions(req).Functions[0].RouteId = "bundle-0"
	result, _ := deploy(t, client, req)
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	var functions []provider.Function
	for _, spec := range vendor.FakeStacks().Provisioned() {
		functions = append(functions, fake.ProvisionedFunctions(spec)...)
	}
	staged := stager.records()
	if len(staged) != 1 || len(functions) != 1 {
		t.Fatalf("the deploy staged %d records and provisioned %d functions, want one of each", len(staged), len(functions))
	}
	if staged[0].Origin != functions[0].URL || staged[0].Origin == "" {
		t.Errorf("origin = %q, want the root function's URL %q: the edge that runs code reaches the deployment there", staged[0].Origin, functions[0].URL)
	}
}

func relayRoutedDeploy(t *testing.T, client contractv1connect.ProviderServiceClient) *progressv1.OperationResult {
	t.Helper()
	req := deployRequest()
	req.Edge = &contractv1.EdgeSelection{Kind: string(fake.KindRelay)}
	webFunctions(req).Functions[0].RouteId = "bundle-0"
	result, _ := deploy(t, client, req)
	return result
}

func TestARecordBehindAnEdgeThatRunsCodeNamesTheRouteTableItsProviderStoredByDigest(t *testing.T) {
	builtProject(t)
	table := []byte("{\"routes\": [{\"id\": \"bundle-0\", \"source\": \"^/(?<slug>[^/]+)$\"}]}")
	builtRoutingApp(t, "web", buildoutput.Hosting{RouteTable: edge.RouteTableNext, RootFunction: "bundle-0", FrameworkBuildID: "b1"}, table)
	client, vendor := deployServed(t)
	stager := staging(t, vendor)

	if result := relayRoutedDeploy(t, client); result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	staged := stager.records()
	if len(staged) != 1 || staged[0].RouteTable == nil {
		t.Fatalf("records %+v, want one naming its route table", staged)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, table); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(compact.Bytes())
	if key := staged[0].RouteTable.Key; !strings.HasSuffix(key, "/route-table/"+hex.EncodeToString(digest[:])+".json") {
		t.Errorf("routeTable.key = %q, want the release's route-table key named by the digest of the compact table", key)
	}
	if staged[0].RouteTable.Format != edge.RouteTableNext {
		t.Errorf("routeTable.format = %q, want %q", staged[0].RouteTable.Format, edge.RouteTableNext)
	}
	stored, found := vendor.FakeStacks().RouteTable(staged[0].RouteTable.Key)
	if !found || !bytes.Equal(stored, compact.Bytes()) {
		t.Errorf("the provider stored %q at the record's key (found %v), want the compact table %q", stored, found, compact.Bytes())
	}
}

func TestADeployWhoseProviderStoredNoRouteTableWhereTheEdgeReadsIsRefusedNamingBootstrap(t *testing.T) {
	builtProject(t)
	builtRoutingApp(t, "web", buildoutput.Hosting{RouteTable: edge.RouteTableNext, RootFunction: "bundle-0", FrameworkBuildID: "b1"}, []byte(`{"routes":[{"id":"bundle-0"}]}`))
	client, vendor := deployServed(t)
	vendor.FakeStacks().WithholdRouteTables()
	stager := staging(t, vendor)

	result := relayRoutedDeploy(t, client)
	if result == nil || result.GetSuccess() {
		t.Fatal("Deploy() succeeded, want it refused: the edge would have no route table to read")
	}
	if !strings.Contains(result.GetError(), "bootstrap") {
		t.Errorf("Deploy() error = %q, want it to say to re-run bootstrap", result.GetError())
	}
	if staged := stager.records(); len(staged) != 0 {
		t.Errorf("records %+v, want none staged naming a table no store holds", staged)
	}
}

func TestARecordBehindAnEdgeThatRunsNoCodeNamesNoRouteTable(t *testing.T) {
	builtProject(t)
	builtRoutingApp(t, "web", buildoutput.Hosting{RouteTable: edge.RouteTableNext, RootFunction: "bundle-0", FrameworkBuildID: "b1"}, []byte(`{"routes":[{"id":"bundle-0"}]}`))
	client, vendor := deployServed(t)
	stager := staging(t, vendor)

	req := deployRequest()
	req.Edge = &contractv1.EdgeSelection{Kind: string(fake.KindDirect)}
	webFunctions(req).Functions[0].RouteId = "bundle-0"
	result, _ := deploy(t, client, req)
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	staged := stager.records()
	if len(staged) != 1 || staged[0].RouteTable != nil {
		t.Errorf("records %+v, want one naming no route table: the root function routes the app with the table it ships", staged)
	}
}

func TestAServerlessAppBehindAnEdgeThatRunsNoCodeRecordsNoOrigin(t *testing.T) {
	builtProject(t)
	builtRoutingApp(t, "web", buildoutput.Hosting{RouteTable: edge.RouteTableNext, RootFunction: "bundle-0", FrameworkBuildID: "b1"}, []byte(`{"routes":[{"id":"bundle-0"}]}`))
	client, vendor := deployServed(t)
	stager := staging(t, vendor)

	req := deployRequest()
	req.Edge = &contractv1.EdgeSelection{Kind: string(fake.KindDirect)}
	webFunctions(req).Functions[0].RouteId = "bundle-0"
	result, _ := deploy(t, client, req)
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	staged := stager.records()
	if len(staged) != 1 || staged[0].Origin != "" {
		t.Errorf("records %+v, want one with no origin: a function origin reads as a container to the edges that reach an origin by URL", staged)
	}
}

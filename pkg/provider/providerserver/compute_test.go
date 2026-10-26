package providerserver_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/ledger"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

const containerTestImage = "ocel/shop/web@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func containerDeployRequest(probe string) *contractv1.DeployRequest {
	req := deployRequest()
	req.Manifest.Apps[0].Artifact = &contractv1.ManifestApp_Container{Container: &contractv1.ContainerArtifact{
		Image:           containerTestImage,
		HealthCheckPath: probe,
		MinInstances:    1,
		MaxInstances:    1,
	}}
	return req
}

func namingARegistry(req *contractv1.DeployRequest) *contractv1.DeployRequest {
	req.ProjectRegistry = &contractv1.ImageRegistry{
		Server:    "ghcr.io",
		Namespace: "acme",
		Username:  "acme-bot",
		Password:  "hunter2",
	}
	return req
}

func TestTheWireAcceptsAnAppNamingContainer(t *testing.T) {
	daemonWithTheBuiltImage(t, "amd64")
	builtProject(t)
	client, _ := deployServed(t)

	req := namingARegistry(containerDeployRequest("/"))

	result, _, err := deployStream(t, client, req)
	if err != nil {
		t.Fatalf("Deploy() with an app naming %q: error = %v, want the wire pin to admit it — it is the only compute the VPS provider runs", provider.ComputeContainer, err)
	}
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() with an app naming %q = %q, want it to succeed", provider.ComputeContainer, result.GetError())
	}
}

func TestTheWireRefusesAnAppThatCarriesNoArtifact(t *testing.T) {
	client, _ := contractServed(t, "1.0.0")

	req := deployRequest()
	req.Manifest.Apps[0].Artifact = nil

	_, _, err := deployStream(t, client, req)
	if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Fatalf("Deploy() with an app carrying neither functions nor an image: code = %v, want %v — the manifest pin is what gives every app a compute by the time a provider reads it", got, connect.CodeInvalidArgument)
	}
}

func TestTheAppSpecNamesTheImageAndProbeAContainerAppIsProvisionedFrom(t *testing.T) {
	daemonWithTheBuiltImage(t, "amd64")
	builtProject(t)
	p := fake.NewProvider(fake.Options{})
	client := servedBy(t, p)

	result, _ := deploy(t, client, namingARegistry(containerDeployRequest("/healthz")))
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	specs := p.FakeStacks().Provisioned()
	app := specs[len(specs)-1].App
	if app == nil {
		t.Fatal("the last plan the stacks port saw provisions no app")
	}
	if app.Compute != provider.ComputeContainer {
		t.Errorf("Compute = %q, want %q: the primitive is chosen by what the plan names", app.Compute, provider.ComputeContainer)
	}
	if app.Image != pushedCoordinate {
		t.Errorf("Image = %q, want %q: the plan names the coordinate the push wrote, not the ref the build left in the local store", app.Image, pushedCoordinate)
	}
	if app.HealthCheckPath != "/healthz" {
		t.Errorf("HealthCheckPath = %q, want the path the manifest says the process is probed at", app.HealthCheckPath)
	}
}

func TestTheAppSpecNamesTheInstanceCountsAContainerAppRunsBetween(t *testing.T) {
	daemonWithTheBuiltImage(t, "amd64")
	builtProject(t)
	p := fake.NewProvider(fake.Options{})
	client := servedBy(t, p)

	req := namingARegistry(containerDeployRequest("/"))
	req.Manifest.Apps[0].GetContainer().MinInstances = 2
	req.Manifest.Apps[0].GetContainer().MaxInstances = 5
	result, _ := deploy(t, client, req)
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	specs := p.FakeStacks().Provisioned()
	if got, want := specs[len(specs)-1].App.Instances, (provider.Instances{Min: 2, Max: 5}); got != want {
		t.Errorf("Instances = %+v, want %+v, the counts the manifest names", got, want)
	}
}

func TestTheWireRefusesAContainerWhoseFloorIsAboveItsCeiling(t *testing.T) {
	client, _ := contractServed(t, "1.0.0")

	req := containerDeployRequest("/")
	req.Manifest.Apps[0].GetContainer().MinInstances = 3
	req.Manifest.Apps[0].GetContainer().MaxInstances = 2

	_, _, err := deployStream(t, client, req)
	if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Fatalf("Deploy() with a container running at least 3 and at most 2: code = %v, want %v", got, connect.CodeInvalidArgument)
	}
}

func TestAServerlessAppSpecNamesItsComputeAndNoImage(t *testing.T) {
	builtProject(t)
	p := fake.NewProvider(fake.Options{})
	client := servedBy(t, p)

	result, _ := deploy(t, client, deployRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	specs := p.FakeStacks().Provisioned()
	app := specs[len(specs)-1].App
	if app.Compute != provider.ComputeServerless {
		t.Errorf("Compute = %q, want %q", app.Compute, provider.ComputeServerless)
	}
	if app.Image != "" || app.HealthCheckPath != "" {
		t.Errorf("Image = %q and HealthCheckPath = %q, want a serverless app to have neither", app.Image, app.HealthCheckPath)
	}
}

func TestTheContainerAProvisionedAppRunsOnIsRecordedAgainstItsStack(t *testing.T) {
	daemonWithTheBuiltImage(t, "amd64")
	builtProject(t)
	p := fake.NewProvider(fake.Options{})
	client := servedBy(t, p)

	result, _ := deploy(t, client, namingARegistry(containerDeployRequest("/")))
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	entries, err := stackrecords.List(context.Background(), p.KeyValues(), environment.TierProduction, "shop")
	if err != nil {
		t.Fatal(err)
	}
	stacks := 0
	for _, entry := range entries {
		if entry.Kind != provider.StackApp {
			continue
		}
		stacks++
		if len(entry.Containers) != 1 || entry.Containers[0].Name != "web" {
			t.Fatalf("%s recorded %v, want the container it ran the app as: nothing can take down what was never written", entry.Name, entry.Containers)
		}
		if entry.Containers[0].Image != pushedCoordinate {
			t.Errorf("%s recorded the container running %q, want %q: a teardown that cannot say what a container ran cannot sweep the image it ran", entry.Name, entry.Containers[0].Image, pushedCoordinate)
		}
	}
	if stacks == 0 {
		t.Fatalf("the deploy wrote no app stack at all among %d entries, so the container it started was recorded nowhere", len(entries))
	}
}

func TestTheLedgerRecordAContainerDeployStagesIsTheOneItsPromotionLooksUp(t *testing.T) {
	daemonWithTheBuiltImage(t, "amd64")
	builtProject(t)
	client, vendor := deployServed(t)

	if result, _ := deploy(t, client, namingARegistry(containerDeployRequest("/"))); !result.GetSuccess() {
		t.Fatalf("Deploy() of a container app = %q", result.GetError())
	}

	releases := ledger.New(vendor.KeyValues(), environment.TierProduction, "shop")
	active, promoted, err := releases.ReadActive(context.Background(), "")
	if err != nil || !promoted {
		t.Fatalf("ReadActive() = %v, %v, want the promotion the deploy made", promoted, err)
	}
	build := active.Releases["web"]
	record, found, err := releases.Record(context.Background(), "web", build)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatalf("the ledger has no record under %q, which is what the promotion names for the container app: every rollback and every re-point would refuse by name", build)
	}
	if record.Image != pushedCoordinate {
		t.Errorf("the record under %q names image %q, want %q, the image a promotion reading it puts in front of the app", build, record.Image, pushedCoordinate)
	}
	if record.Origin == "" || record.Origin != "https://"+record.Physical+".ctr.fake.invalid" {
		t.Errorf("the record under %q names origin %q for container %q, want the URL the provider serves the container on: an edge that fronts a container by URL has nothing else to reach", build, record.Origin, record.Physical)
	}
}

func TestAContainerARouterAddressesItselfIsReportedAtItsOwnURLWhenNoDomainIsDeclared(t *testing.T) {
	daemonWithTheBuiltImage(t, "amd64")
	builtProject(t)
	client, vendor := deployServed(t)
	vendor.Edges().(*fake.Edges).Edge(fake.KindRelay).AddressesItself(true)

	req := namingARegistry(containerDeployRequest("/"))
	req.Manifest.Domains = nil
	result, _ := deploy(t, client, req)
	if !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}
	record := activeRecord(t, vendor, "web")
	if got, want := servedAppURLs(result, "web"), []string{record.Origin}; record.Origin == "" || !slices.Equal(got, want) {
		t.Errorf("the deploy reported %v, want %v: with no domain declared, the address the router gave the app is the only one it is served on", got, want)
	}
}

func activeRecord(t *testing.T, vendor *fake.Provider, app string) router.ReleaseRecord {
	t.Helper()
	releases := ledger.New(vendor.KeyValues(), environment.TierProduction, "shop")
	active, promoted, err := releases.ReadActive(context.Background(), "")
	if err != nil || !promoted {
		t.Fatalf("ReadActive() = %v, %v, want the promotion the deploy made", promoted, err)
	}
	record, found, err := releases.Record(context.Background(), app, active.Releases[app])
	if err != nil || !found {
		t.Fatalf("Record(%q) = %v, %v, want the record the promotion names", app, found, err)
	}
	return record
}

func functionContainerDeployRequest(functions ...*contractv1.ManifestFunction) *contractv1.DeployRequest {
	req := namingARegistry(deployRequest())
	req.Manifest.Apps[0].Artifact = &contractv1.ManifestApp_Container{Container: &contractv1.ContainerArtifact{
		HealthCheckPath: "/",
		MinInstances:    1,
		MaxInstances:    1,
		Functions:       functions,
	}}
	return req
}

func builtServerFunction() *contractv1.ManifestFunction {
	return &contractv1.ManifestFunction{
		LogicalName:  "server",
		Framework:    &contractv1.Framework{Name: "next", Arch: "x86_64"},
		EntryFile:    "index.handler",
		ArtifactPath: artifactPath,
		RouteId:      "server",
	}
}

func TestTheAppSpecOfAContainerBuiltFromAFunctionRunsTheImageBuiltFromThatFunction(t *testing.T) {
	stagedProject(t, "web", "admin")
	static := filepath.Join(buildoutput.AppRoot(workingOutputRoot(t), "web"), "static", "_next", "static", "app.js")
	if err := os.MkdirAll(filepath.Dir(static), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(static, []byte("chunk"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := fake.NewProvider(fake.Options{}).CarryingBuildPaths("static")
	client := servedBy(t, p)

	result, _ := deploy(t, client, functionContainerDeployRequest(builtServerFunction()))
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	specs := p.FakeStacks().Provisioned()
	app := specs[len(specs)-1].App
	if app.Compute != provider.ComputeContainer {
		t.Errorf("Compute = %q, want %q", app.Compute, provider.ComputeContainer)
	}
	pushes := specs[len(specs)-1].Images.Pushes
	if len(pushes) != 1 || pushes[0].Function || pushes[0].App != "web" || pushes[0].Built == nil || app.Image != pushes[0].ImageRef {
		t.Fatalf("Image = %q with pushes %+v, want the container to run the one image built from its function", app.Image, pushes)
	}
	if got := regularFiles(t, pushes[0].Built); !slices.Contains(got, "ocel/runtime/next/entrypoint.mjs") || !slices.Contains(got, "ocel/app/"+builtEntrypoint) || !slices.Contains(got, "ocel/build/static/_next/static/app.js") {
		t.Errorf("the container's image holds %v, want the function, the runtime the provider declares for it and the build's static files", got)
	}
	if len(app.Functions) != 1 || app.Functions[0].Name != "server" || app.Functions[0].ArtifactDir == "" {
		t.Errorf("Functions = %+v, want the one built function the container runs, with the directory it was built into", app.Functions)
	}
	if app.Instances != (provider.Instances{Min: 1, Max: 1}) || app.HealthCheckPath != "/" {
		t.Errorf("Instances = %+v and HealthCheckPath = %q, want the counts and probe the manifest names", app.Instances, app.HealthCheckPath)
	}
}

func TestAContainerBuiltFromAFunctionCarriesNoBuildPathItsProviderDoesNotRead(t *testing.T) {
	stagedProject(t, "web", "admin")
	static := filepath.Join(buildoutput.AppRoot(workingOutputRoot(t), "web"), "static", "_next", "static", "app.js")
	if err := os.MkdirAll(filepath.Dir(static), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(static, []byte("chunk"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := fake.NewProvider(fake.Options{})

	result, _ := deploy(t, servedBy(t, p), functionContainerDeployRequest(builtServerFunction()))
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	specs := p.FakeStacks().Provisioned()
	pushes := specs[len(specs)-1].Images.Pushes
	if len(pushes) != 1 || pushes[0].Built == nil {
		t.Fatalf("pushes = %+v, want the one image built from the function", pushes)
	}
	for _, file := range regularFiles(t, pushes[0].Built) {
		if strings.HasPrefix(file, "ocel/build/") {
			t.Errorf("the container's image holds %s, want no build file: a provider that uploads its static files would ship them twice", file)
		}
	}
}

func TestTheRecordOfAContainerBuiltFromAFunctionServesThatFunctionAtTheContainersURL(t *testing.T) {
	stagedProject(t, "web", "admin")
	p := fake.NewProvider(fake.Options{})
	client := servedBy(t, p)

	if result, _ := deploy(t, client, functionContainerDeployRequest(builtServerFunction())); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}

	record := activeRecord(t, p, "web")
	if record.Origin == "" || record.FunctionURLs["server"] != record.Origin {
		t.Errorf("FunctionURLs = %v with origin %q, want the function served at the container's URL: an edge that dispatches by route table reaches a function by its URL", record.FunctionURLs, record.Origin)
	}
}

func TestTheUsesOfAContainerBuiltFromAFunctionReachItsProvidersPreflight(t *testing.T) {
	stagedProject(t, "web", "admin")
	declaresNeed(t, "web", edge.NeedRevalidation)
	vendor := &addingParts{Provider: fake.NewProvider(fake.Options{})}
	client := servedBy(t, vendor)

	req := functionContainerDeployRequest(builtServerFunction())
	req.Dry = true
	if result, _ := deploy(t, client, req); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}
	if len(vendor.seen) == 0 {
		t.Fatal("the preflight never ran")
	}
	if uses := vendor.seen[0].Uses; !slices.ContainsFunc(uses, func(u buildoutput.Uses) bool { return u.Revalidation }) {
		t.Errorf("the preflight was handed uses %+v, want the container that revalidates among them: its build says so as a serverless build does", uses)
	}
}

func TestTheWireRefusesAContainerThatIsNotOneImageOrOneFunction(t *testing.T) {
	second := builtServerFunction()
	second.LogicalName = "api"
	imageAndFunction := functionContainerDeployRequest(builtServerFunction())
	imageAndFunction.Manifest.Apps[0].GetContainer().Image = containerTestImage
	for name, req := range map[string]*contractv1.DeployRequest{
		"no image and no function": functionContainerDeployRequest(),
		"two functions":            functionContainerDeployRequest(builtServerFunction(), second),
		"an image and a function":  imageAndFunction,
	} {
		t.Run(name, func(t *testing.T) {
			client, _ := contractServed(t, "1.0.0")

			_, _, err := deployStream(t, client, req)
			if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
				t.Fatalf("Deploy() with a container of %s: code = %v, want %v", name, got, connect.CodeInvalidArgument)
			}
		})
	}
}

func TestAContainerBuiltFromAFunctionForAnotherArchitectureIsRefused(t *testing.T) {
	stagedProject(t, "web", "admin")
	client := servedBy(t, fake.NewProvider(fake.Options{}))
	fn := builtServerFunction()
	fn.Framework.Arch = "arm64"

	_, _, err := deployStream(t, client, functionContainerDeployRequest(fn))
	if connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "build it for linux/amd64") {
		t.Fatalf("Deploy() = %v, want a refusal naming the architecture the target runs", err)
	}
}

func TestAContainerBuiltFromAFunctionIsDispatchedByTheEdgeFrontingItsOwnRouter(t *testing.T) {
	for name, fronts := range map[string]bool{"an edge that fronts its origins": true, "an edge that forwards to them": false} {
		t.Run(name, func(t *testing.T) {
			stagedProject(t, "web", "admin")
			builtRoutingApp(t, "web", buildoutput.Hosting{RouteTable: edge.RouteTableNext, RootFunction: "server"}, []byte(`{"routes":[]}`))
			vendor := fake.NewProvider(fake.Options{})
			relay := vendor.Edges().(*fake.Edges).Edge(fake.KindRelay)
			relay.ProxiesRecords()
			if fronts {
				relay.FrontsOrigins()
			}
			client := servedBy(t, frontedByCompute{vendor})
			req := functionContainerDeployRequest(builtServerFunction())
			req.Edge = &contractv1.EdgeSelection{Kind: string(fake.KindRelay)}

			if result, _ := deploy(t, client, req); !result.GetSuccess() {
				t.Fatalf("Deploy() = %q", result.GetError())
			}

			specs := vendor.FakeStacks().Provisioned()
			app := specs[len(specs)-1].App
			if (app.EdgeRouteTable != nil) != fronts || (app.Routing != nil) == fronts {
				t.Errorf("Routing = %+v and EdgeRouteTable = %+v behind %s, want the edge's code to dispatch the container only where it fronts its router", app.Routing, app.EdgeRouteTable, name)
			}
		})
	}
}

func TestAContainerBuiltFromAFunctionDispatchesItsOwnPathsWhenItsEdgeForwardsIt(t *testing.T) {
	for name, dispatches := range map[string]bool{"a router that dispatches": true, "a router that forwards": false} {
		t.Run(name, func(t *testing.T) {
			stagedProject(t, "web", "admin")
			builtRoutingApp(t, "web", buildoutput.Hosting{RouteTable: edge.RouteTableNext, RootFunction: "server"}, []byte(`{"routes":[]}`))
			vendor := fake.NewProvider(fake.Options{})
			vendor.Edges().(*fake.Edges).Edge(fake.KindRelay).RouterDispatches(dispatches)
			client := servedBy(t, vendor)
			req := functionContainerDeployRequest(builtServerFunction())
			req.Edge = &contractv1.EdgeSelection{Kind: string(fake.KindRelay)}

			if result, _ := deploy(t, client, req); !result.GetSuccess() {
				t.Fatalf("Deploy() = %q", result.GetError())
			}

			specs := vendor.FakeStacks().Provisioned()
			app := specs[len(specs)-1].App
			if dispatched := app.Routing != nil; dispatched == dispatches || (app.EdgeRouteTable != nil) != dispatches {
				t.Errorf("Routing = %+v and EdgeRouteTable = %+v behind %s, want the container to dispatch its own paths only where the edge does not", app.Routing, app.EdgeRouteTable, name)
			}
		})
	}
}

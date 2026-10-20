package providerserver_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	variablestorev1 "github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1/variablestorev1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/providerserver"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/pkg/seal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/pkg/variablestore"
)

const webBuildID = "0123456789abcdef0123456789abcdef"

const artifactPath = "apps/web/functions/server.func"

const adminArtifactPath = "apps/admin/functions/server.func"

const builtEntrypoint = "index.mjs"

func appArtifactPath(app string) string {
	return "apps/" + app + "/functions/server.func"
}

func builtProject(t *testing.T) {
	t.Helper()
	builtApps(t, "web", "admin")
}

func builtApps(t *testing.T, apps ...string) {
	t.Helper()
	root := t.TempDir()
	builtAppsUnder(t, root, apps...)
	t.Chdir(root)
}

func builtAppsUnder(t *testing.T, root string, apps ...string) {
	t.Helper()
	for _, app := range apps {
		built := filepath.Join(root, buildoutput.StateDir, "output", filepath.FromSlash(appArtifactPath(app)))
		if err := os.MkdirAll(built, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(built, builtEntrypoint), []byte("a built function"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDeployReadsTheBuildUnderTheOutputRootTheRequestNamesWhateverDirectoryItRunsIn(t *testing.T) {
	project := t.TempDir()
	builtAppsUnder(t, project, "web", "admin")
	t.Chdir(t.TempDir())

	vendor := fake.NewProvider(fake.Options{Region: "nowhere"})
	server := httptest.NewServer(providerserver.ConformanceMux(providerserver.Config{
		Version: "1.0.0",
		New:     func(context.Context, provider.Settings) (provider.Provider, error) { return vendor, nil },
	}))
	t.Cleanup(server.Close)
	client := contractv1connect.NewProviderServiceClient(server.Client(), server.URL)
	if _, err := client.Configure(context.Background(), &contractv1.ConfigureRequest{
		Config: &contractv1.ProviderConfig{ProjectDir: project},
	}); err != nil {
		t.Fatalf("Configure() error = %v", err)
	}
	bootstrappedOverRPC(t, client)

	req := deployRequest()
	root, err := buildoutput.Root(project)
	if err != nil {
		t.Fatal(err)
	}
	req.OutputRoot = root
	result, _ := deploy(t, client, req)
	if !result.GetSuccess() {
		t.Fatalf("Deploy() from outside the project = %q, want it to ship the build under the output root the CLI named", result.GetError())
	}
}

func TestADeployShippingAppsThatNamesNoOutputRootIsRefusedBeforeItReadsAnyBuild(t *testing.T) {
	builtProject(t)
	client := servedProvider(t, "1.0.0", fake.NewProvider(fake.Options{Region: "nowhere"}))
	bootstrappedOverRPC(t, client)

	req := deployRequest()
	req.OutputRoot = filepath.Join("relative", "build")
	_, _, err := deployStream(t, client, req)
	if code, refused := provider.RefusedCode(err); !refused || code != refusal.CodeInvalid || !strings.Contains(err.Error(), "build output root") {
		t.Fatalf("Deploy() naming a relative output root = %v, want it refused as invalid: the CLI names the absolute directory the build it ships lives in", err)
	}
}

func deployRequest() *contractv1.DeployRequest {
	return &contractv1.DeployRequest{
		Manifest: &contractv1.Manifest{
			Slug: "shop",
			Resources: []*contractv1.ManifestResource{{
				LogicalName: "orders",
				Resource: &resourcesv1.ResourceIdentifier{
					Type: resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES,
					Name: "orders",
				},
			}},
			Usages: []*contractv1.ManifestUsage{{App: "web", Resource: "orders"}},
			Domains: []*contractv1.TierDomains{{
				Tier:      environmentv1.Tier_TIER_PRODUCTION,
				Hostnames: []string{"shop.example"},
			}},
			Apps: []*contractv1.ManifestApp{{
				Name:      "web",
				Framework: &contractv1.Framework{Name: "next"},
				BuildId:   webBuildID,
				Artifact: serverless(&contractv1.ManifestFunction{
					LogicalName:  "server",
					Framework:    &contractv1.Framework{Name: "next"},
					EntryFile:    "index.handler",
					ArtifactPath: artifactPath,
				}),
			}},
		},
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION},
	}
}

func rooted(req *contractv1.DeployRequest) *contractv1.DeployRequest {
	if req.GetOutputRoot() != "" {
		return req
	}
	dir, err := os.Getwd()
	if err != nil {
		return req
	}
	if root, err := buildoutput.Root(dir); err == nil {
		req.OutputRoot = root
	}
	return req
}

func serverless(functions ...*contractv1.ManifestFunction) *contractv1.ManifestApp_Serverless {
	return &contractv1.ManifestApp_Serverless{Serverless: &contractv1.ServerlessArtifact{Functions: functions}}
}

func webFunctions(req *contractv1.DeployRequest) *contractv1.ServerlessArtifact {
	return req.GetManifest().GetApps()[0].GetServerless()
}

func deploy(t *testing.T, client contractv1connect.ProviderServiceClient, req *contractv1.DeployRequest) (*progressv1.OperationResult, []*progressv1.OperationEvent) {
	t.Helper()
	result, events, err := deployStream(t, client, req)
	if err != nil {
		t.Fatalf("Deploy() stream error = %v", err)
	}
	return result, events
}

func deployStream(
	t *testing.T,
	client contractv1connect.ProviderServiceClient,
	req *contractv1.DeployRequest,
) (*progressv1.OperationResult, []*progressv1.OperationEvent, error) {
	t.Helper()
	stream, err := client.Deploy(context.Background(), rooted(req))
	if err != nil {
		t.Fatalf("Deploy() error = %v", err)
	}
	defer stream.Close()

	var events []*progressv1.OperationEvent
	var result *progressv1.OperationResult
	for stream.Receive() {
		event := stream.Msg()
		events = append(events, event)
		if got := event.GetResult(); got != nil {
			result = got
		}
	}
	return result, events, stream.Err()
}

func TestDeployProvisionsInfraThenAppsAndPromotes(t *testing.T) {
	builtProject(t)
	client, p := deployServed(t)

	result, events := deploy(t, client, deployRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}
	if result.GetPromotionId() == "" {
		t.Error("Deploy() promoted nothing: the result names no promotion, so nothing can be rolled back to")
	}
	if published := storedBindings(t, p); len(published) != 1 || published["orders"].Owner != variablestore.OwnerOcel {
		t.Fatalf("Deploy() published %v, want only orders, the one the manifest declares", published)
	}

	if events[0].GetStarted() == nil {
		t.Fatalf("the first event is %T, want a started scope: the CLI draws the tree before any work reports into it", events[0].GetBody())
	}

	specs := p.FakeStacks().Provisioned()
	if len(specs) != 2 {
		t.Fatalf("the stacks port saw %d specs, want the infra stack and the app stack", len(specs))
	}
	if specs[0].Kind != provider.StackInfra || specs[1].Kind != provider.StackApp {
		t.Fatalf("the stacks port saw %s then %s, want infra before the apps that binding to it", specs[0].Kind, specs[1].Kind)
	}
	if !slices.ContainsFunc(specs[1].App.Grants, func(binding provider.Binding) bool { return binding.Name == "orders" }) {
		t.Errorf("the app spec grants %v, want the infra binding the app binds a client to", specs[1].App.Grants)
	}
	if specs[1].App.Functions[0].ArtifactDir == "" {
		t.Error("the app spec has a function naming no build artifact, so its provider has nothing to ship")
	}
	if specs[1].App.BuildID != webBuildID {
		t.Errorf("the app spec names build %q, want %q: dispatch serves the build the CLI built under this id", specs[1].App.BuildID, webBuildID)
	}
}

func TestADeployReportsTheStorageCoordinateItWroteEachAppUnder(t *testing.T) {
	builtProject(t)
	client, p := deployServed(t)

	result, _ := deploy(t, client, deployRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}
	coordinate := result.GetApps()[0].GetStorageCoordinate()
	if !regexp.MustCompile(`^shop/prod/web/r[0-9a-f]{8}$`).MatchString(coordinate) {
		t.Fatalf("the result reports storage coordinate %q for web, want shop/prod/web/<release token>", coordinate)
	}
	key := p.FakeStacks().Shipped(p.FakeStacks().Provisioned()[1].Ref)[0].Ref.Key
	if !strings.HasPrefix(key, string(naming.ObjectFunction)+"/"+coordinate+"/") {
		t.Errorf("web's function artifact is %q, want it under the reported coordinate %q", key, coordinate)
	}
}

func TestADeployReportsTheReleaseItMadeLiveForEachApp(t *testing.T) {
	builtProject(t)
	client, p := deployServed(t)

	result, _ := deploy(t, client, deployRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}
	release := result.GetApps()[0].GetRelease()
	parsed, err := provider.ParseRelease(release)
	if err != nil {
		t.Fatalf("the result reports release %q for web, want a release: %v", release, err)
	}
	if got := p.FakeStacks().Provisioned()[1].App.BuildID; parsed.BuildID() != got {
		t.Errorf("web's reported release is of build %q, want the build %q the app stack served", parsed.BuildID(), got)
	}
}

func TestADeployReportsWhenThePromotionItMadeLiveWasRecorded(t *testing.T) {
	builtProject(t)
	client, p := deployServed(t)

	result, _ := deploy(t, client, deployRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}
	active, ok, err := p.Releases(environment.TierProduction, "shop").ReadActive(context.Background(), router.DefaultPointer)
	if err != nil || !ok {
		t.Fatalf("read the active promotion: ok %v err %v", ok, err)
	}
	if got := result.GetPromotedAtSeconds(); got != active.Ts {
		t.Errorf("the result says the promotion went live at %d, want the %d the router recorded", got, active.Ts)
	}
}

func TestDeployRefusesToPublishABlanketGrantWithoutAskingTheProvider(t *testing.T) {
	builtProject(t)
	client, p := deployServed(t)
	p.FakeStacks().Grants = []provider.Grant{{
		Label:     "everything",
		Actions:   []string{"*"},
		Resources: []string{"*"},
	}}

	stream, err := client.Deploy(context.Background(), rooted(deployRequest()))
	if err != nil {
		t.Fatalf("Deploy() error = %v", err)
	}
	defer stream.Close()
	for stream.Receive() {
	}
	err = stream.Err()
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("Deploy() = %v, want it refused: a provider that vets no grant still may not publish one over every resource", err)
	}
	if !strings.Contains(err.Error(), "every action") {
		t.Fatalf("Deploy() = %v, want it to name the wildcard providerserver refuses", err)
	}
}

const adminBuildID = "fedcba9876543210fedcba9876543210"

func twoAppRequest() *contractv1.DeployRequest {
	req := deployRequest()
	manifest := req.GetManifest()
	manifest.Resources = append(manifest.Resources, &contractv1.ManifestResource{
		LogicalName: "uploads",
		Resource: &resourcesv1.ResourceIdentifier{
			Type: resourcesv1.ResourceType_RESOURCE_TYPE_BUCKET,
			Name: "uploads",
		},
	})
	manifest.Apps = append(manifest.Apps, &contractv1.ManifestApp{
		Name:      "admin",
		Framework: &contractv1.Framework{Name: "next"},
		BuildId:   adminBuildID,
		Artifact: serverless(&contractv1.ManifestFunction{
			LogicalName:  "admin-server",
			Framework:    &contractv1.Framework{Name: "next"},
			EntryFile:    "index.handler",
			ArtifactPath: adminArtifactPath,
		}),
	})
	manifest.Usages = append(manifest.Usages,
		&contractv1.ManifestUsage{App: "admin", Resource: "orders"},
		&contractv1.ManifestUsage{App: "admin", Resource: "uploads"})
	return req
}

func grantNames(spec provider.StackSpec) []string {
	names := make([]string, 0, len(spec.App.Grants))
	for _, binding := range spec.App.Grants {
		names = append(names, binding.Name)
	}
	slices.Sort(names)
	return names
}

func TestDeployGrantsAnAppOnlyWhatItsUsageEdgesName(t *testing.T) {
	builtProject(t)
	client, p := deployServed(t)

	if result, _ := deploy(t, client, twoAppRequest()); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}

	apps := map[string]provider.StackSpec{}
	for _, spec := range p.FakeStacks().Provisioned() {
		if spec.App != nil {
			apps[spec.App.App] = spec
		}
	}
	if len(apps) != 2 {
		t.Fatalf("the stacks port provisioned %d apps, want web and admin", len(apps))
	}

	if want := []string{"orders", "uploads"}; !slices.Equal(grantNames(apps["admin"]), want) {
		t.Errorf("admin is granted %v, want %v: it names both in its usage edges", grantNames(apps["admin"]), want)
	}
	if want := []string{"orders"}; !slices.Equal(grantNames(apps["web"]), want) {
		t.Errorf("web is granted %v, want %v; a compromise of web must expose no credential it never needed", grantNames(apps["web"]), want)
	}
	for _, binding := range apps["web"].App.Values.Bindings {
		if binding.Name == "uploads" {
			t.Error("web is handed the bucket's address for a bucket it never uses")
		}
	}
}

func TestDeployGrantsNothingToAnAppWithNoUsageEdge(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)

	req := twoAppRequest()
	manifest := req.GetManifest()
	manifest.Usages = slices.DeleteFunc(manifest.Usages, func(usage *contractv1.ManifestUsage) bool {
		return usage.GetApp() == "admin"
	})
	if result, _ := deploy(t, client, req); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}

	for _, spec := range vendor.FakeStacks().Provisioned() {
		if spec.App == nil || spec.App.App != "admin" {
			continue
		}
		if len(spec.App.Grants) != 0 || len(spec.App.Values.Bindings) != 0 {
			t.Errorf("admin is granted %v, want nothing for an app with no usage edge at all", spec.App.Grants)
		}
	}
}

func TestDeployRecordsEveryStackItProvisioned(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)

	if result, _ := deploy(t, client, deployRequest()); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}

	entries, err := stackrecords.List(context.Background(), vendor.KeyValues(), environment.TierProduction, "shop")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("the project records %d stacks, want the infra stack and one app stack", len(entries))
	}
	var infra, app stackrecords.NamedStack
	for _, entry := range entries {
		if entry.Name.IsInfra() {
			infra = entry
			continue
		}
		app = entry
	}
	if len(infra.Bindings) != 1 || infra.Bindings[0].Name != "orders" {
		t.Errorf("the infra stack records bindings %v, want the resource it provisioned", infra.Bindings)
	}
	if app.App != "web" || app.Release == "" {
		t.Errorf("the app stack records %+v, want it named for the app and the build it serves", app.Stack)
	}
	if len(app.Functions) != 1 {
		t.Errorf("the app stack records %d functions, want the one it provisioned", len(app.Functions))
	}
}

func TestDeployUploadsEveryFunctionArtifact(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)

	if result, _ := deploy(t, client, deployRequest()); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}

	specs := vendor.FakeStacks().Provisioned()
	ref := vendor.FakeStacks().Shipped(specs[1].Ref)[0].Ref
	opened, err := vendor.Artifacts().Open(context.Background(), ref)
	if err != nil {
		t.Fatalf("Open(%+v) after the deploy = %v, want the artifact stored where the plan named it", ref, err)
	}
	defer opened.Close()
	if !strings.HasPrefix(ref.Key, "fn/shop/prod/web/") {
		t.Errorf("the artifact landed at %q, want it under the release's own prefix", ref.Key)
	}
}

func TestDeployPublishesEveryInfraBindingForItsAppsToRead(t *testing.T) {
	builtProject(t)
	client, p := deployServed(t)

	if result, _ := deploy(t, client, deployRequest()); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}
	if result, _ := deploy(t, client, deployRequest()); !result.GetSuccess() {
		t.Fatalf("a second Deploy() = %q", result.GetError())
	}

	specs := p.FakeStacks().Provisioned()
	last := specs[len(specs)-1]
	if !slices.ContainsFunc(last.App.Grants, func(binding provider.Binding) bool {
		return binding.Name == "orders" && binding.Properties[provider.PropertyHost] != ""
	}) {
		t.Fatalf("a second deploy grants %v, want the published binding read back whole", last.App.Grants)
	}
}

type refusingStacks struct {
	*fake.Provider
	stacks provider.Stacks
}

func (r refusingStacks) Stacks() provider.Stacks { return r.stacks }

type halfBindingStacks struct{}

func (halfBindingStacks) Plan(ctx context.Context, spec provider.StackSpec, _ progress.Log) (provider.Plan, error) {
	return resources.SynthesizedPlan(ctx, spec, provider.StackResult{})
}

func (halfBindingStacks) PlanDestroy(_ context.Context, ref provider.StackRef, _ progress.Log) (provider.Plan, error) {
	return resources.SynthesizedRemoval(ref, provider.StackResult{}), nil
}

func (halfBindingStacks) Provision(_ context.Context, spec provider.StackSpec, _ progress.Log) (provider.StackResult, error) {
	var result provider.StackResult
	for _, resource := range spec.Resources {
		result.Bindings = append(result.Bindings, provider.Binding{
			Type:       resource.Type,
			Name:       resource.Name,
			Properties: map[string]string{provider.PropertyHost: "db.invalid"},
		})
	}
	return result, nil
}

func (halfBindingStacks) Destroy(context.Context, provider.StackDestroy, progress.Log) error {
	return nil
}

func TestDeployRefusesABindingMissingAPropertyBeforeItRecordsItsBindings(t *testing.T) {
	builtProject(t)
	base := fake.NewProvider(fake.Options{})
	client := servedBy(t, refusingStacks{Provider: base, stacks: halfBindingStacks{}})

	stream, err := client.Deploy(context.Background(), rooted(deployRequest()))
	if err != nil {
		t.Fatal(err)
	}
	for stream.Receive() {
	}
	err = stream.Err()
	stream.Close()

	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("Deploy() with a Postgres binding with only a host = %v, want it refused as invalid", err)
	}
	if !strings.Contains(err.Error(), provider.PropertyPort) {
		t.Errorf("Deploy() failed with %q, want it to name the property that is missing", err)
	}
	entries, err := stackrecords.List(context.Background(), base.KeyValues(), environment.TierProduction, "shop")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if len(entry.Bindings) != 0 || entry.SpecDigest != "" {
			t.Errorf("the refused deploy recorded %s with bindings %v and digest %q, want neither written for a binding providerserver would not accept", entry.Name, entry.Bindings, entry.SpecDigest)
		}
	}
}

type countingCipher struct {
	seal.Cipher

	mu     sync.Mutex
	opened int
}

func (c *countingCipher) Open(ctx context.Context, tier environment.Tier, bound seal.AssociatedData, sealed []byte) ([]byte, error) {
	if bytes.HasPrefix(bound.Bytes(), []byte("7:binding,")) {
		c.mu.Lock()
		c.opened++
		c.mu.Unlock()
	}
	return c.Cipher.Open(ctx, tier, bound, sealed)
}

func (c *countingCipher) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.opened
}

type sealCounting struct {
	*fake.Provider
	cipher *countingCipher
}

func (s sealCounting) Cipher() seal.Cipher { return s.cipher }

func TestDeployResolvesThePublishedBindingsOnce(t *testing.T) {
	builtProject(t)
	base := fake.NewProvider(fake.Options{})
	cipher := &countingCipher{Cipher: base.Cipher()}
	client := servedBy(t, sealCounting{Provider: base, cipher: cipher})

	if result, _ := deploy(t, client, twoAppRequest()); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}

	if opened := cipher.count(); opened != 2 {
		t.Fatalf("the deploy opened %d sealed binding values, want one per published binding: "+
			"two apps over two bindings resolve the same set, and the run reads it once", opened)
	}
}

type resolvingStacks struct {
	inner provider.Stacks

	mu       sync.Mutex
	host     string
	resolved []provider.Binding
}

func (r *resolvingStacks) publishes(host string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.host = host
}

func (r *resolvingStacks) Resolved() []provider.Binding {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.resolved)
}

func (r *resolvingStacks) Plan(ctx context.Context, spec provider.StackSpec, progress progress.Log) (provider.Plan, error) {
	return r.inner.Plan(ctx, spec, progress)
}

func (r *resolvingStacks) PlanDestroy(ctx context.Context, ref provider.StackRef, progress progress.Log) (provider.Plan, error) {
	return r.inner.PlanDestroy(ctx, ref, progress)
}

func (r *resolvingStacks) Provision(ctx context.Context, spec provider.StackSpec, progress progress.Log) (provider.StackResult, error) {
	result, err := r.inner.Provision(ctx, spec, progress)
	if err != nil {
		return result, err
	}
	if spec.Kind == provider.StackInfra {
		r.mu.Lock()
		defer r.mu.Unlock()
		for _, binding := range result.Bindings {
			binding.Properties[provider.PropertyHost] = r.host
		}
		return result, nil
	}
	binding, err := spec.Bindings.Named(ctx, "orders")
	if err != nil {
		return provider.StackResult{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.resolved = append(r.resolved, binding)
	return result, nil
}

func (r *resolvingStacks) Destroy(ctx context.Context, req provider.StackDestroy, progress progress.Log) error {
	return r.inner.Destroy(ctx, req, progress)
}

func TestDeployProvisionsInfraBeforeEveryAppSoATransformReadsThisDeploysBinding(t *testing.T) {
	builtProject(t)
	base := fake.NewProvider(fake.Options{})
	stacks := &resolvingStacks{inner: base.Stacks(), host: "db-one.invalid"}
	client := servedBy(t, refusingStacks{Provider: base, stacks: stacks})

	if result, _ := deploy(t, client, deployRequest()); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}
	stacks.publishes("db-two.invalid")
	changed := deployRequest()
	changed.Manifest.Resources = append(changed.Manifest.Resources, bucketResource("uploads"))
	if result, _ := deploy(t, client, changed); !result.GetSuccess() {
		t.Fatalf("a second Deploy() = %q", result.GetError())
	}

	resolved := stacks.Resolved()
	if len(resolved) != 2 {
		t.Fatalf("the app stack resolved %d bindings over two deploys, want one per deploy", len(resolved))
	}
	if host := resolved[0].Properties[provider.PropertyHost]; host != "db-one.invalid" {
		t.Fatalf("the first deploy's app stack resolved orders at %q, want the binding its own infra stack published: "+
			"the app stack is provisioned after the infra stack, so the record it reads is the one this deploy just wrote", host)
	}
	if host := resolved[1].Properties[provider.PropertyHost]; host != "db-two.invalid" {
		t.Errorf("the second deploy's app stack resolved orders at %q, want %q: the app stack read a binding its own deploy replaced, "+
			"so provisioning ran an app before the infra it reads from", host, "db-two.invalid")
	}
}

func servedBy(t *testing.T, p provider.Provider) contractv1connect.ProviderServiceClient {
	t.Helper()
	config := providerserver.Config{
		Version: "1.0.0",
		New:     func(context.Context, provider.Settings) (provider.Provider, error) { return p, nil },
	}
	server := httptest.NewServer(providerserver.ConformanceMux(config))
	t.Cleanup(server.Close)

	client := contractv1connect.NewProviderServiceClient(server.Client(), server.URL)
	if _, err := client.Configure(context.Background(), configureInWorkingDir(t)); err != nil {
		t.Fatalf("Configure() error = %v", err)
	}
	bootstrappedOverRPC(t, client)
	return client
}

func deployServed(t *testing.T) (contractv1connect.ProviderServiceClient, *fake.Provider) {
	t.Helper()
	client, vendor := contractServed(t, "1.0.0")
	bootstrappedOverRPC(t, client)
	return client, vendor
}

func bootstrappedOverRPC(t *testing.T, client contractv1connect.ProviderServiceClient) {
	t.Helper()
	bootstrapOK(t, client, &contractv1.BootstrapRequest{
		Tier: environmentv1.Tier_TIER_PRODUCTION,
	})
}

func declaresNeed(t *testing.T, app string, need edge.Need) {
	t.Helper()
	dir := buildoutput.AppRoot(workingOutputRoot(t), app)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(buildoutput.Hosting{
		Version: buildoutput.HostingVersion,
		Needs:   map[edge.Need]buildoutput.NeedDetail{need: {Routes: []string{"/feed"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, buildoutput.HostingFile), raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDeployWaivesANeedTheProjectAllowsToDegrade(t *testing.T) {
	builtProject(t)
	declaresNeed(t, "web", edge.NeedStreaming)
	client, vendor := deployServed(t)
	vendor.Edges().(*fake.Edges).Edge(fake.KindRelay).Serves(nil)

	req := deployRequest()
	req.Edge = &contractv1.EdgeSelection{
		Kind:          string(fake.KindRelay),
		AllowDegraded: []string{string(edge.NeedStreaming)},
	}

	result, events := deploy(t, client, req)
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() waiving %s = %q, want the deploy to succeed degraded", edge.NeedStreaming, result.GetError())
	}
	if !slices.ContainsFunc(events, func(event *progressv1.OperationEvent) bool {
		return event.GetLevel() == progressv1.Level_LEVEL_WARN && strings.HasPrefix(event.GetMessage(), string(edge.NeedStreaming)+" runs degraded: ")
	}) {
		t.Errorf("the deploy said nothing about %s, want the waived need reported out loud", edge.NeedStreaming)
	}
}

func TestADeployWarnsInTheCheckPhaseNamingTheAppItDegrades(t *testing.T) {
	builtProject(t)
	declaresNeed(t, "web", edge.NeedStreaming)
	client, vendor := deployServed(t)
	vendor.Edges().(*fake.Edges).Edge(fake.KindRelay).Serves(nil)

	req := deployRequest()
	req.Edge = &contractv1.EdgeSelection{
		Kind:          string(fake.KindRelay),
		AllowDegraded: []string{string(edge.NeedStreaming)},
	}

	_, events := deploy(t, client, req)
	i := slices.IndexFunc(events, func(event *progressv1.OperationEvent) bool {
		return event.GetLevel() == progressv1.Level_LEVEL_WARN && strings.HasPrefix(event.GetMessage(), string(edge.NeedStreaming)+" runs degraded: ")
	})
	if i < 0 {
		t.Fatalf("the deploy said nothing about %s, want the waived need reported", edge.NeedStreaming)
	}
	degraded := events[i]
	if degraded.GetLevel() != progressv1.Level_LEVEL_WARN || degraded.GetPhase() != progressv1.Phase_PHASE_CHECK {
		t.Errorf("the degraded need is %v in %v, want WARN in PHASE_CHECK", degraded.GetLevel(), degraded.GetPhase())
	}
	if degraded.GetSubject() != "web" {
		t.Errorf("the degraded need's subject is %q, want the app %q", degraded.GetSubject(), "web")
	}
	want := "streaming runs degraded: responses are buffered before they leave the origin the way `next start` answers without an edge in front, " +
		"so the first byte waits on the last. It affects routes /feed"
	if degraded.GetMessage() != want {
		t.Errorf("the degraded need reads %q, want %q", degraded.GetMessage(), want)
	}
}

func TestADeployWarnsInTheCheckPhaseNamingTheEdgeWhenItCannotTellWhetherThePlanRunsCode(t *testing.T) {
	builtProject(t)
	declaresNeed(t, "web", edge.NeedEdgeMiddleware)
	client, vendor := deployServed(t)
	vendor.Edges().(*fake.Edges).Edge(fake.KindRelay).Entitles(edge.CodeEntitlement{
		Granted: edge.EntitlementUnknown,
		Reason:  "the token cannot read billing",
	})

	req := deployRequest()
	req.Edge = &contractv1.EdgeSelection{Kind: string(fake.KindRelay)}

	result, events := deploy(t, client, req)
	if !result.GetSuccess() {
		t.Fatalf("Deploy() with an unknown entitlement = %q, want it to proceed", result.GetError())
	}
	i := slices.IndexFunc(events, func(event *progressv1.OperationEvent) bool {
		return strings.Contains(event.GetMessage(), "the token cannot read billing")
	})
	if i < 0 {
		t.Fatal("the deploy never said why it could not confirm the plan runs code at the edge")
	}
	warning := events[i]
	if warning.GetLevel() != progressv1.Level_LEVEL_WARN || warning.GetPhase() != progressv1.Phase_PHASE_CHECK {
		t.Errorf("the warning is %v in %v, want WARN in PHASE_CHECK", warning.GetLevel(), warning.GetPhase())
	}
	if warning.GetSubject() != string(fake.KindRelay) {
		t.Errorf("the warning's subject is %q, want the edge %q", warning.GetSubject(), fake.KindRelay)
	}
}

func TestDeployRefusesANeedTheProjectDoesNotWaive(t *testing.T) {
	builtProject(t)
	declaresNeed(t, "web", edge.NeedStreaming)
	client, vendor := deployServed(t)
	vendor.Edges().(*fake.Edges).Edge(fake.KindRelay).Serves(nil)

	req := deployRequest()
	req.Edge = &contractv1.EdgeSelection{Kind: string(fake.KindRelay)}

	stream, err := client.Deploy(context.Background(), rooted(req))
	if err != nil {
		t.Fatal(err)
	}
	var failure string
	for stream.Receive() {
		if result := stream.Msg().GetResult(); result != nil {
			failure = result.GetError()
		}
	}
	said := failure + connectMessage(stream.Err())
	stream.Close()

	if !strings.Contains(said, string(edge.NeedStreaming)) {
		t.Fatalf("Deploy() against an edge serving nothing = %q, want it refused by the need's name", said)
	}
}

func operatorServed(t *testing.T) (contractv1connect.ProviderServiceClient, variablestorev1connect.VariableStoreServiceClient) {
	t.Helper()
	p := fake.NewProvider(fake.Options{})
	config := providerserver.Config{
		Version: "1.0.0",
		New:     func(context.Context, provider.Settings) (provider.Provider, error) { return p, nil },
	}
	server := httptest.NewServer(providerserver.ConformanceMux(config))
	t.Cleanup(server.Close)

	deploys := contractv1connect.NewProviderServiceClient(server.Client(), server.URL)
	if _, err := deploys.Configure(context.Background(), configureInWorkingDir(t)); err != nil {
		t.Fatalf("Configure() error = %v", err)
	}
	bootstrappedOverRPC(t, deploys)
	return deploys, variablestorev1connect.NewVariableStoreServiceClient(server.Client(), server.URL)
}

func TestDeployPublishesBindingsWhereTheOperatorReadsThem(t *testing.T) {
	builtProject(t)
	deploys, variables := operatorServed(t)
	ctx := context.Background()

	if result, _ := deploy(t, deploys, deployRequest()); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}

	listed, err := variables.ListBindings(ctx, &variablestorev1.ListBindingsRequest{
		Slug: "shop",
		Tier: environmentv1.Tier_TIER_PRODUCTION,
	})
	if err != nil {
		t.Fatalf("ListBindings() = %v", err)
	}
	if len(listed.GetBindings()) != 1 || listed.GetBindings()[0].GetName() != "orders" {
		t.Fatalf("ListBindings() after a production deploy = %+v, want the binding the deploy published", listed.GetBindings())
	}

	removed, err := variables.RemoveBinding(ctx, &variablestorev1.RemoveBindingRequest{
		Slug: "shop",
		Tier: environmentv1.Tier_TIER_PRODUCTION,
		Name: "orders",
	})
	if err != nil || !removed.GetRemoved() {
		t.Fatalf("RemoveBinding() = %+v, %v, want the published binding taken away", removed, err)
	}
}

func TestDeployPrunesTheBindingItStoppedProvisioning(t *testing.T) {
	builtProject(t)
	deploys, variables := operatorServed(t)
	ctx := context.Background()

	if result, _ := deploy(t, deploys, deployRequest()); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}
	dropped := deployRequest()
	dropped.Manifest.Resources = nil
	dropped.Manifest.Usages = nil
	if result, _ := deploy(t, deploys, dropped); !result.GetSuccess() {
		t.Fatalf("Deploy() without the resource = %q", result.GetError())
	}

	listed, err := variables.ListBindings(ctx, &variablestorev1.ListBindingsRequest{
		Slug: "shop",
		Tier: environmentv1.Tier_TIER_PRODUCTION,
	})
	if err != nil {
		t.Fatalf("ListBindings() = %v", err)
	}
	if len(listed.GetBindings()) != 0 {
		t.Fatalf("ListBindings() = %+v, want nothing: the deploy stopped provisioning the resource, so its record and credentials go with it", listed.GetBindings())
	}
}

func TestDeployLeavesAnotherPublishersBindingAlone(t *testing.T) {
	builtProject(t)
	deploys, variables := operatorServed(t)
	ctx := context.Background()

	if _, err := variables.SetBinding(ctx, &variablestorev1.SetBindingRequest{
		Slug:  "shop",
		Tier:  environmentv1.Tier_TIER_PRODUCTION,
		Owner: "acme",
		Published: &variablestorev1.PublishedBinding{Binding: &bindingsv1.Binding{
			Name:       "warehouse",
			Source:     "acme",
			Properties: &bindingsv1.Binding_Postgres{Postgres: &bindingsv1.PostgresProperties{Host: "db.acme"}},
		}},
	}); err != nil {
		t.Fatalf("SetBinding() = %v", err)
	}

	if result, _ := deploy(t, deploys, deployRequest()); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}

	listed, err := variables.ListBindings(ctx, &variablestorev1.ListBindingsRequest{
		Slug: "shop",
		Tier: environmentv1.Tier_TIER_PRODUCTION,
	})
	if err != nil {
		t.Fatalf("ListBindings() = %v", err)
	}
	names := make([]string, 0, len(listed.GetBindings()))
	for _, binding := range listed.GetBindings() {
		names = append(names, binding.GetName())
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"orders", "warehouse"}) {
		t.Fatalf("ListBindings() = %v, want ocel's own binding beside the one acme published", names)
	}
}

func TestDeployRecordsThePartsItsProjectDependsOn(t *testing.T) {
	builtProject(t)
	client, _ := deployServed(t)
	ctx := context.Background()

	if result, _ := deploy(t, client, deployRequest()); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}

	stream, err := client.Bootstrap(ctx, &contractv1.BootstrapRequest{
		Tier:   environmentv1.Tier_TIER_PRODUCTION,
		Edge:   &contractv1.EdgeSelection{Kind: string(fake.KindDirect)},
		Remove: []string{fake.PartImages},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := drain(stream)
	said := result.GetError() + connectMessage(err)
	if !strings.Contains(said, "shop") {
		t.Fatalf("removing the part its edge requires = %q, want it refused for the project that depends on them", said)
	}
}

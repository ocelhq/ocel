package providerserver_test

import (
	"context"
	"encoding/json"
	"errors"
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
	"github.com/ocelhq/ocel/pkg/keyvalue"
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
	"github.com/ocelhq/ocel/pkg/statedir"
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
		built := filepath.Join(root, statedir.Name, "output", filepath.FromSlash(appArtifactPath(app)))
		if err := os.MkdirAll(built, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(built, builtEntrypoint), []byte("a built function"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDeployReadsTheBuildUnderTheConfiguredProjectWhateverDirectoryItRunsIn(t *testing.T) {
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

	result, _ := deploy(t, client, deployRequest())
	if !result.GetSuccess() {
		t.Fatalf("Deploy() from outside the project = %q, want it to ship the build under the project the CLI configured", result.GetError())
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
	stream, err := client.Deploy(context.Background(), req)
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
	if specs[1].App.Functions[0].Artifact.Key == "" {
		t.Error("the app spec has a function with no artifact, so the upload never reached the release")
	}
	if specs[1].App.BuildID != webBuildID {
		t.Errorf("the app spec names build %q, want %q: dispatch serves the build the CLI built under this id", specs[1].App.BuildID, webBuildID)
	}
}

func TestADeployReportsTheStoragePrefixItWroteEachAppUnder(t *testing.T) {
	builtProject(t)
	client, p := deployServed(t)

	result, _ := deploy(t, client, deployRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}
	prefix := result.GetApps()[0].GetStoragePrefix()
	if !regexp.MustCompile(`^prod/shop/web/r[0-9a-f]{8}/$`).MatchString(prefix) {
		t.Fatalf("the result reports storage prefix %q for web, want prod/shop/web/<release token>/", prefix)
	}
	if key := p.FakeStacks().Provisioned()[1].App.Functions[0].Artifact.Key; !strings.HasPrefix(key, prefix) {
		t.Errorf("web's function artifact is %q, want it under the reported prefix %q", key, prefix)
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

	stream, err := client.Deploy(context.Background(), deployRequest())
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
	ref := specs[1].App.Functions[0].Artifact
	opened, err := vendor.Artifacts().Open(context.Background(), ref)
	if err != nil {
		t.Fatalf("Open(%+v) after the deploy = %v, want the artifact stored where the plan named it", ref, err)
	}
	defer opened.Close()
	if !strings.HasPrefix(ref.Key, "prod/shop/web/") {
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
	return resources.SynthesizedPlan(ctx, fake.NewArtifacts(), spec, provider.StackResult{})
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

func (halfBindingStacks) Destroy(context.Context, provider.StackRef, provider.ImageStore, progress.Log) error {
	return nil
}

func TestDeployRefusesABindingMissingAPropertyBeforeItRecordsItsBindings(t *testing.T) {
	builtProject(t)
	base := fake.NewProvider(fake.Options{})
	client := servedBy(t, refusingStacks{Provider: base, stacks: halfBindingStacks{}})

	stream, err := client.Deploy(context.Background(), deployRequest())
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
		if len(entry.Bindings) != 0 || entry.ResourceDigest != "" {
			t.Errorf("the refused deploy recorded %s with bindings %v and digest %q, want neither written for a binding providerserver would not accept", entry.Name, entry.Bindings, entry.ResourceDigest)
		}
	}
}

type countingCipher struct {
	seal.Cipher

	mu     sync.Mutex
	opened int
}

func (c *countingCipher) Open(ctx context.Context, tier environment.Tier, bound seal.AssociatedData, sealed []byte) ([]byte, error) {
	if slices.ContainsFunc(bound, func(f seal.Field) bool { return f.Name == "binding" && f.Value != "" }) {
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

func (r *resolvingStacks) Destroy(ctx context.Context, ref provider.StackRef, images provider.ImageStore, progress progress.Log) error {
	return r.inner.Destroy(ctx, ref, images, progress)
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
	if result, _ := deploy(t, client, deployRequest()); !result.GetSuccess() {
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
		Tier:     environmentv1.Tier_TIER_PRODUCTION,
		Features: []string{fake.FeatureCache, fake.FeatureImages},
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

	stream, err := client.Deploy(context.Background(), req)
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
		Binding: &bindingsv1.Binding{
			Name:       "warehouse",
			Source:     "acme",
			Properties: &bindingsv1.Binding_Postgres{Postgres: &bindingsv1.PostgresProperties{Host: "db.acme"}},
		},
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

func TestDeployRecordsTheFeaturesItsProjectDependsOn(t *testing.T) {
	builtProject(t)
	client, _ := deployServed(t)
	ctx := context.Background()

	if result, _ := deploy(t, client, deployRequest()); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}

	planned, err := client.DescribeBootstrap(ctx, &contractv1.DescribeBootstrapRequest{
		Tier:           environmentv1.Tier_TIER_PRODUCTION,
		WithDependents: true,
	})
	if err != nil {
		t.Fatalf("DescribeBootstrap() error = %v", err)
	}
	for _, feature := range planned.GetFeatures() {
		if feature.GetName() != fake.FeatureCache {
			continue
		}
		if !slices.Contains(feature.GetDependents(), "shop") {
			t.Errorf("%s reports dependents %v, want the project deployed against it", fake.FeatureCache, feature.GetDependents())
		}
	}

	stream, err := client.Bootstrap(ctx, &contractv1.BootstrapRequest{
		Tier:   environmentv1.Tier_TIER_PRODUCTION,
		Edge:   &contractv1.EdgeSelection{Kind: string(fake.KindDirect)},
		Remove: []string{fake.FeatureCache},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := drain(stream)
	said := result.GetError() + connectMessage(err)
	if !strings.Contains(said, "shop") {
		t.Fatalf("removing every feature = %q, want it refused for the project that depends on them", said)
	}
}

func servedURLs(result *progressv1.OperationResult) []string {
	var urls []string
	for _, app := range result.GetApps() {
		urls = append(urls, app.GetUrls()...)
	}
	return urls
}

func findDeploymentURL(result *progressv1.OperationResult, app string) string {
	for _, appResult := range result.GetApps() {
		if appResult.GetApp() == app {
			return appResult.GetDeploymentUrl()
		}
	}
	return ""
}

func servedAppURLs(result *progressv1.OperationResult, app string) []string {
	for _, appResult := range result.GetApps() {
		if appResult.GetApp() == app {
			return appResult.GetUrls()
		}
	}
	return nil
}

func hostnameAdded(t *testing.T, client contractv1connect.ProviderServiceClient, hosts ...string) {
	t.Helper()
	stream, err := client.AddHostname(context.Background(), &contractv1.HostnameRequest{
		Slug:       "shop",
		Configured: configuredHosts(hosts...),
		Edge:       &contractv1.EdgeSelection{Dns: &contractv1.Dns{Kind: string(fake.KindZone), Zone: "shop.example"}},
	})
	if err != nil {
		t.Fatalf("AddHostname() error = %v", err)
	}
	result, err := drain(stream)
	if err != nil || !result.GetSuccess() {
		t.Fatalf("AddHostname() = %q, %v", result.GetError(), err)
	}
}

func writtenBy(zone string) *contractv1.EdgeSelection {
	return &contractv1.EdgeSelection{Dns: &contractv1.Dns{Kind: string(fake.KindZone), Zone: zone}}
}

func TestTheFirstDeployAttachesAHostnameItsDNSWriterPoints(t *testing.T) {
	builtProject(t)
	client, _ := deployServed(t)

	req := deployRequest()
	req.Edge = writtenBy("shop.example")
	result, _ := deploy(t, client, req)
	if !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}
	if !slices.Equal(servedURLs(result), []string{"https://shop.example"}) || noteOf(result) != "" {
		t.Errorf("the first deploy returned urls %v and the note %q, want the hostname it attached printed: no record was left to write by hand, so nothing waits on anyone",
			servedURLs(result), noteOf(result))
	}
}

func TestTheFirstDeployAttachesALocalhostNameWithNoDNSWriter(t *testing.T) {
	builtProject(t)
	client, _ := deployServed(t)

	req := deployRequest()
	req.Manifest.Domains[0].Hostnames = []string{"web-j-1-deploy-python.localhost"}
	result, events := deploy(t, client, req)
	if !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}
	if manual := manualRecordsIn(events); len(manual) != 0 {
		t.Errorf("the deploy asked for manual records %v, want none: a localhost name resolves without any record", manual)
	}
	if want := []string{"https://web-j-1-deploy-python.localhost"}; !slices.Equal(servedURLs(result), want) || noteOf(result) != "" {
		t.Errorf("the deploy printed %v with the note %q, want %v", servedURLs(result), noteOf(result), want)
	}
}

func TestADeployBindsItsHostnamesOnlyOnceEveryStackItProvisionsIsUp(t *testing.T) {
	builtProject(t)
	client, _ := deployServed(t)

	req := deployRequest()
	req.Edge = writtenBy("shop.example")
	result, events := deploy(t, client, req)
	if !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}
	var said []string
	bound, provisioned := -1, -1
	for _, event := range events {
		message := saidLine(event)
		switch {
		case strings.HasPrefix(message, "Binding shop.example") && bound < 0:
			bound = len(said)
		case strings.HasPrefix(message, "Provisioned stack "):
			provisioned = len(said)
		}
		if message != "" {
			said = append(said, message)
		}
	}
	if bound < 0 || provisioned < 0 || bound < provisioned {
		t.Errorf("the deploy said %v, want shop.example bound only after every stack was provisioned: an edge binds what already exists, such as the box's store name", said)
	}
}

func TestADeployDeclaringAWildcardForProductionIsRefusedBeforeItProvisionsAnything(t *testing.T) {
	builtProject(t)
	client, p := deployServed(t)

	req := deployRequest()
	req.Manifest.Domains[0].Hostnames = []string{"*.shop.example"}
	_, _, err := deployStream(t, client, req)
	if code, _ := provider.RefusedCode(err); code != refusal.CodeInvalid || !strings.Contains(err.Error(), "domains.preview") {
		t.Fatalf("Deploy() = %v, want it refused as invalid: a wildcard belongs to domains.preview", err)
	}
	if specs := p.FakeStacks().Provisioned(); len(specs) != 0 {
		t.Errorf("the refused deploy provisioned %d stacks, want none: the hostname is read before anything is built", len(specs))
	}
}

func TestADeployLeavesAHostnameAnotherEdgeServesToDomainAdd(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)

	req := deployRequest()
	req.Edge = writtenBy("shop.example")
	if result, _ := deploy(t, client, req); !result.GetSuccess() {
		t.Fatalf("Deploy() on the %s edge = %q", fake.KindRelay, result.GetError())
	}

	req.Edge.Kind = string(fake.KindDirect)
	result, _ := deploy(t, client, req)
	if !result.GetSuccess() {
		t.Fatalf("Deploy() on the %s edge = %q", fake.KindDirect, result.GetError())
	}
	note := noteOf(result)
	if !strings.Contains(note, "shop.example") || !strings.Contains(note, string(fake.KindRelay)) || !strings.Contains(note, "`ocel domain add`") {
		t.Errorf("the note = %q, want it naming the hostname, the edge that still serves it, and that `ocel domain add` moves it", note)
	}
	edges := vendor.Edges().(*fake.Edges)
	if bound := edges.Edge(fake.KindDirect).Bindings(); len(bound) != 0 {
		t.Errorf("the %s edge binds %v, want nothing: moving a hostname between edges is `ocel domain add`'s to order", fake.KindDirect, bound)
	}
	if bound := edges.Edge(fake.KindRelay).Bindings(); len(bound) != 1 || bound[0].Hostname != "shop.example" {
		t.Errorf("the %s edge binds %v, want shop.example still bound there: nothing moved it off", fake.KindRelay, bound)
	}
}

func TestALaterDeployAttachesAHostnameTheConfigNewlyDeclares(t *testing.T) {
	builtProject(t)
	client, _ := deployServed(t)

	req := deployRequest()
	req.Edge = writtenBy("shop.example")
	if result, _ := deploy(t, client, req); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}

	req.Manifest.Domains[0].Hostnames = append(req.Manifest.Domains[0].Hostnames, "www.shop.example")
	result, _ := deploy(t, client, req)
	if !result.GetSuccess() {
		t.Fatalf("a deploy declaring one more hostname = %q, want it attached: the config is the declaration and the deploy reconciles it", result.GetError())
	}
	if want := []string{"https://shop.example", "https://www.shop.example"}; !slices.Equal(servedURLs(result), want) {
		t.Errorf("the deploy printed %v, want %v", servedURLs(result), want)
	}
}

func TestADeployWhoseDNSWriterFailsPromotesNothing(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	writer, err := vendor.DNS().Open(fake.KindZone, "shop.example", "")
	if err != nil {
		t.Fatal(err)
	}
	writer.(*fake.DNSRecords).Refuse(errors.New("the zone's api answered 500"))

	req := deployRequest()
	req.Edge = writtenBy("shop.example")
	result, events := deploy(t, client, req)
	if result.GetSuccess() {
		t.Fatalf("Deploy() succeeded, want it failed: the dns writer broke, which no one waiting fixes")
	}
	if _, promoted := spanStatuses(events)[promotionSpan]; promoted {
		t.Error("the run promoted, want nothing promoted once attaching a declared hostname failed")
	}
}

func TestADeployRefusedForAReasonNoWaitingFixesFailsWithThatReason(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	reason := "no load balancer exists in this project for shop.example: run `ocel bootstrap`"
	vendor.RefuseCertificates(refusal.Refuse(refusal.CodeNotReady, "%s", reason))

	req := deployRequest()
	req.Edge = writtenBy("shop.example")
	result, events := deploy(t, client, req)
	if result.GetSuccess() {
		t.Fatalf("Deploy() succeeded with the note %q, want it failed: a missing load balancer waits on `ocel bootstrap`, not on `ocel domain add`", noteOf(result))
	}
	if !strings.Contains(result.GetError(), reason) {
		t.Errorf("Deploy() = %q, want the refusal's own remedy", result.GetError())
	}
	if _, promoted := spanStatuses(events)[promotionSpan]; promoted {
		t.Error("the run promoted, want nothing promoted over a hostname that could not be attached")
	}
}

func TestADeployWhoseCertificateIsStillIssuingLeavesItToDomainAdd(t *testing.T) {
	builtProject(t)
	client, p := deployServed(t)
	p.RequireValidationRecords(edge.Record{Name: "_acme.shop.example", Type: edge.RecordTypeCNAME, Value: "validate.example"})
	p.StallAfterProving(provider.Resumable(refusal.Refuse(refusal.CodeNotReady, "the certificate is still validating")))

	req := deployRequest()
	req.Edge = writtenBy("shop.example")
	result, _ := deploy(t, client, req)
	if !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed: an issuance still in flight delays the hostname, not the release", result.GetError())
	}
	if !strings.Contains(noteOf(result), "the certificate is still validating") {
		t.Errorf("the note = %q, want it naming the issuance it waits on", noteOf(result))
	}
}

func TestADeployWhoseCertificateWaitsOnYouLeavesItToDomainAdd(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	vendor.RequireValidationRecords(edge.Record{Name: "_acme.shop.example", Type: edge.RecordTypeCNAME, Value: "validate.example"})

	result, events := deploy(t, client, deployRequest())
	if !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed without waiting on a record only you can write", result.GetError())
	}
	if manual := manualRecordsIn(events); !slices.Contains(manual, "_acme.shop.example CNAME") {
		t.Errorf("the deploy asked for manual records %v, want the record that proves the certificate", manual)
	}
	if len(servedURLs(result)) != 0 || !strings.Contains(noteOf(result), "Prove you own shop.example") {
		t.Errorf("the deploy printed %v with the note %q, want no url and a note naming the proof it waits on",
			servedURLs(result), noteOf(result))
	}
}

func manualRecordsIn(events []*progressv1.OperationEvent) []string {
	var manual []string
	for _, event := range events {
		for _, record := range event.GetDnsManualRecords().GetRecords() {
			manual = append(manual, record.GetName()+" "+record.GetType())
		}
	}
	return manual
}

func TestDeployNeedingManualRecordsSucceedsAndLeavesTheHostnameToDomainAdd(t *testing.T) {
	builtProject(t)
	client, _ := deployServed(t)

	result, events := deploy(t, client, deployRequest())
	if !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed: a record only you can write delays the hostname, not the release", result.GetError())
	}
	if len(servedURLs(result)) != 0 {
		t.Errorf("the deploy printed %v, want no url: shop.example answers nowhere until its record is written", servedURLs(result))
	}
	if manual := manualRecordsIn(events); !slices.Contains(manual, "shop.example CNAME") {
		t.Errorf("the deploy asked for manual records %v, want the record that points shop.example at the edge, told the way domain add tells it", manual)
	}
	note := noteOf(result)
	if !strings.Contains(note, "shop.example") || !strings.Contains(note, "ocel domain add") || strings.Contains(note, "deploy again") {
		t.Errorf("the note = %q, want it naming the hostname, the record to add and that `ocel domain add` resumes it — never another deploy", note)
	}

	hostnameAdded(t, client, "shop.example")
}

func TestDeployAnnouncesThePreviewHostnameOfTheProjectsOwnWildcard(t *testing.T) {
	builtProject(t)
	client, _ := deployServed(t)
	previewBootstrapped(t, client)

	result, _ := deploy(t, client, previewRequest())
	if !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}
	urls := servedURLs(result)
	if len(urls) != 1 || !regexp.MustCompile(`^https://pr-7-[a-z2-7]{24}\.preview\.example$`).MatchString(urls[0]) {
		t.Errorf("the preview deploy announced %v, want pr-7 and a signed token under the project's own wildcard", urls)
	}
}

func TestAPreviewKeepsItsAliasAndServesEachDeploymentOnAHostnameOfItsOwn(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	previewBootstrapped(t, client)

	first, _ := deploy(t, client, previewRequest())
	second, _ := deploy(t, client, previewRequest())
	if !first.GetSuccess() || !second.GetSuccess() {
		t.Fatalf("Deploy() = %q, %q", first.GetError(), second.GetError())
	}
	if !slices.Equal(servedURLs(first), servedURLs(second)) {
		t.Errorf("the alias moved from %v to %v, want one alias for the life of the preview", servedURLs(first), servedURLs(second))
	}
	firstDeployment, secondDeployment := findDeploymentURL(first, "web"), findDeploymentURL(second, "web")
	if firstDeployment == "" || firstDeployment == secondDeployment || slices.Contains(servedURLs(first), firstDeployment) {
		t.Fatalf("deployment urls %q and %q beside alias %v, want each deployment a hostname of its own", firstDeployment, secondDeployment, servedURLs(first))
	}

	plane := vendor.Routers().(*fake.Routers).DataPlane(fake.RouterRelay)
	if pointer, builds := plane.FindServingPointer(strings.TrimPrefix(servedURLs(second)[0], "https://")); pointer != "pr-7" || builds["web"] == "" {
		t.Errorf("the alias is served by %q with %v, want the preview's pointer", pointer, builds)
	}
	want := router.FormatDeploymentPointer("pr-7", first.GetPromotionId())
	if pointer, builds := plane.FindServingPointer(strings.TrimPrefix(firstDeployment, "https://")); pointer != want || builds["web"] == "" {
		t.Errorf("the first deployment's hostname is served by %q with %v after the next deploy, want %s still serving it", pointer, builds, want)
	}
}

func TestPruneAndRemoveStopServingTheHostnamesOfWhatTheyDrop(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	previewBootstrapped(t, client)
	first, _ := deploy(t, client, previewRequest())
	deploy(t, client, previewRequest())
	second, _ := deploy(t, client, previewRequest())
	if !first.GetSuccess() || !second.GetSuccess() {
		t.Fatalf("Deploy() = %q, %q", first.GetError(), second.GetError())
	}
	plane := vendor.Routers().(*fake.Routers).DataPlane(fake.RouterRelay)
	host := func(url string) string { return strings.TrimPrefix(url, "https://") }

	listed, err := client.ListEnvironments(context.Background(), &contractv1.ListEnvironmentsRequest{Slug: "shop"})
	if err != nil {
		t.Fatal(err)
	}
	if got := listed.GetEnvironments(); len(got) != 1 || !slices.Equal(got[0].GetAliasUrls(), servedURLs(second)) {
		t.Errorf("ListEnvironments = %v, want pr-7 listed with its alias %v", got, servedURLs(second))
	}

	pruning, err := client.RemoveStalePromotions(context.Background(), &contractv1.RemoveStalePromotionsRequest{
		Slug: "shop", KeepN: 1, Environment: previewRequest().GetEnvironment(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result, err := drain(pruning); err != nil || !result.GetSuccess() {
		t.Fatalf("RemoveStalePromotions() = %q, %v", result.GetError(), err)
	}
	if pointer, _ := plane.FindServingPointer(host(findDeploymentURL(first, "web"))); pointer != "" {
		t.Errorf("the pruned deployment's hostname is still served by %q, want nothing", pointer)
	}
	for _, kept := range []string{servedURLs(second)[0], findDeploymentURL(second, "web")} {
		if pointer, _ := plane.FindServingPointer(host(kept)); pointer == "" {
			t.Errorf("%s stopped serving, want what the prune kept still served", kept)
		}
	}

	removing, err := client.RemoveEnvironment(context.Background(), &contractv1.RemoveEnvironmentRequest{
		Slug: "shop", Environment: &environmentv1.Environment{
			Tier: environmentv1.Tier_TIER_PREVIEW, Identity: "pr-7", Lifecycle: environmentv1.Lifecycle_LIFECYCLE_PERSISTENT,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result, err := drain(removing); err != nil || !result.GetSuccess() {
		t.Fatalf("RemoveEnvironment() = %q, %v", result.GetError(), err)
	}
	if served := plane.ListServedHostnames(); len(served) != 0 {
		t.Errorf("after the preview was removed %v are still served, want none of its hostnames", served)
	}
}

func TestADeploymentHostnameAPruneFailedToWithdrawIsWithdrawnByRemove(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	previewBootstrapped(t, client)
	first, _ := deploy(t, client, previewRequest())
	deploy(t, client, previewRequest())
	third, _ := deploy(t, client, previewRequest())
	if !first.GetSuccess() || !third.GetSuccess() {
		t.Fatalf("Deploy() = %q, %q", first.GetError(), third.GetError())
	}
	plane := vendor.Routers().(*fake.Routers).DataPlane(fake.RouterRelay)

	plane.FailNextPointerRemoval(errors.New("the data plane is down"))
	pruning, err := client.RemoveStalePromotions(context.Background(), &contractv1.RemoveStalePromotionsRequest{
		Slug: "shop", KeepN: 1, Environment: previewRequest().GetEnvironment(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result, err := drain(pruning); err == nil && result.GetSuccess() {
		t.Fatal("RemoveStalePromotions() succeeded through a refused pointer removal")
	}
	if pointer, _ := plane.FindServingPointer(strings.TrimPrefix(findDeploymentURL(first, "web"), "https://")); pointer == "" {
		t.Fatalf("the pruned deployment's hostname stopped serving through a refused removal, so this test has nothing left to withdraw")
	}

	removing, err := client.RemoveEnvironment(context.Background(), &contractv1.RemoveEnvironmentRequest{
		Slug: "shop", Environment: &environmentv1.Environment{
			Tier: environmentv1.Tier_TIER_PREVIEW, Identity: "pr-7", Lifecycle: environmentv1.Lifecycle_LIFECYCLE_PERSISTENT,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result, err := drain(removing); err != nil || !result.GetSuccess() {
		t.Fatalf("RemoveEnvironment() = %q, %v", result.GetError(), err)
	}
	if served := plane.ListServedHostnames(); len(served) != 0 {
		t.Errorf("after the preview was removed %v are still served, want the deployment hostname the prune failed to withdraw gone too", served)
	}
}

func TestAPruneThatFailedToWithdrawADeploymentHostnameReclaimsItsBuildOnTheRetry(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	previewBootstrapped(t, client)
	first, _ := deploy(t, client, previewRequest())
	deploy(t, client, previewRequest())
	third, _ := deploy(t, client, previewRequest())
	if !first.GetSuccess() || !third.GetSuccess() {
		t.Fatalf("Deploy() = %q, %q", first.GetError(), third.GetError())
	}
	plane := vendor.Routers().(*fake.Routers).DataPlane(fake.RouterRelay)
	prune := func() []string {
		stream, err := client.RemoveStalePromotions(context.Background(), &contractv1.RemoveStalePromotionsRequest{
			Slug: "shop", KeepN: 1, Environment: previewRequest().GetEnvironment(),
		})
		if err != nil {
			t.Fatal(err)
		}
		var destroyed []string
		for _, event := range recorded(stream) {
			if line := saidLine(event); strings.HasPrefix(line, "Destroying the stack of web release") {
				destroyed = append(destroyed, line)
			}
		}
		return destroyed
	}

	plane.FailNextPointerRemoval(errors.New("the data plane is down"))
	if destroyed := prune(); len(destroyed) != 0 {
		t.Fatalf("the refused prune destroyed %v, want nothing destroyed while a hostname still routes to it", destroyed)
	}
	destroyed := prune()
	if pointer, _ := plane.FindServingPointer(strings.TrimPrefix(findDeploymentURL(first, "web"), "https://")); pointer != "" {
		t.Errorf("the retried prune left the pruned deployment's hostname served by %q, want nothing", pointer)
	}
	if len(destroyed) == 0 {
		t.Errorf("the retried prune destroyed nothing, want the releases of the deployments the refused prune dropped")
	}
}

func TestAnAliasTheNextDeployReplacesStopsServingAndRemoveStopsServingEveryAlias(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	previewBootstrapped(t, client)
	plane := vendor.Routers().(*fake.Routers).DataPlane(fake.RouterRelay)
	host := func(url string) string { return strings.TrimPrefix(url, "https://") }

	first, _ := deploy(t, client, previewRequest())
	moved := previewRequest()
	moved.Manifest.Domains[0].Hostnames = []string{"*.moved.example"}
	second, _ := deploy(t, client, moved)
	if !first.GetSuccess() || !second.GetSuccess() {
		t.Fatalf("Deploy() = %q, %q", first.GetError(), second.GetError())
	}
	if slices.Equal(servedURLs(first), servedURLs(second)) {
		t.Fatalf("the alias stayed %v when domains.preview moved, want it on the new wildcard", servedURLs(first))
	}
	if pointer, _ := plane.FindServingPointer(host(servedURLs(first)[0])); pointer != "" {
		t.Errorf("the replaced alias %s is still served by %q, want nothing", servedURLs(first)[0], pointer)
	}
	if pointer, _ := plane.FindServingPointer(host(servedURLs(second)[0])); pointer != "pr-7" {
		t.Errorf("the new alias %s is served by %q, want pr-7", servedURLs(second)[0], pointer)
	}

	plane.FailNextPointerMove(errors.New("the data plane is down"))
	third, _ := deploy(t, client, previewRequest())
	if third.GetSuccess() {
		t.Fatal("Deploy() succeeded through a refused pointer move")
	}
	removing, err := client.RemoveEnvironment(context.Background(), &contractv1.RemoveEnvironmentRequest{
		Slug: "shop", Environment: &environmentv1.Environment{
			Tier: environmentv1.Tier_TIER_PREVIEW, Identity: "pr-7", Lifecycle: environmentv1.Lifecycle_LIFECYCLE_PERSISTENT,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result, err := drain(removing); err != nil || !result.GetSuccess() {
		t.Fatalf("RemoveEnvironment() = %q, %v", result.GetError(), err)
	}
	if served := plane.ListServedHostnames(); len(served) != 0 {
		t.Errorf("after the preview was removed %v are still served, want none of the aliases it ever published", served)
	}
}

func TestAPreviewWhoseAliasMoveFailsIsListedOnTheAliasStillServedAndRemoveWithdrawsBoth(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	previewBootstrapped(t, client)
	plane := vendor.Routers().(*fake.Routers).DataPlane(fake.RouterRelay)

	first, _ := deploy(t, client, previewRequest())
	if !first.GetSuccess() {
		t.Fatalf("Deploy() = %q", first.GetError())
	}
	moved := previewRequest()
	moved.Manifest.Domains[0].Hostnames = []string{"*.moved.example"}
	plane.FailNextPointerMove(errors.New("the data plane is down"))
	if failed, _ := deploy(t, client, moved); failed.GetSuccess() {
		t.Fatal("Deploy() succeeded through a refused pointer move")
	}

	listed, err := client.ListEnvironments(context.Background(), &contractv1.ListEnvironmentsRequest{Slug: "shop"})
	if err != nil {
		t.Fatal(err)
	}
	if got := listed.GetEnvironments(); len(got) != 1 || !slices.Equal(got[0].GetAliasUrls(), servedURLs(first)) {
		t.Errorf("ListEnvironments = %v after the move to *.moved.example failed, want pr-7 listed on %v, the alias still served", got, servedURLs(first))
	}

	removing, err := client.RemoveEnvironment(context.Background(), &contractv1.RemoveEnvironmentRequest{
		Slug: "shop", Environment: &environmentv1.Environment{
			Tier: environmentv1.Tier_TIER_PREVIEW, Identity: "pr-7", Lifecycle: environmentv1.Lifecycle_LIFECYCLE_PERSISTENT,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result, err := drain(removing); err != nil || !result.GetSuccess() {
		t.Fatalf("RemoveEnvironment() = %q, %v", result.GetError(), err)
	}
	if served := plane.ListServedHostnames(); len(served) != 0 {
		t.Errorf("after the preview was removed %v are still served, want every alias it published or tried to", served)
	}
}

func dryEnsurePreviewAlias(t *testing.T, client contractv1connect.ProviderServiceClient) map[string]string {
	t.Helper()
	ensured, err := client.EnsurePreviewAlias(context.Background(), &contractv1.EnsurePreviewAliasRequest{
		Slug:        "shop",
		Environment: previewRequest().GetEnvironment(),
		Token:       "abcdefghijklmnop",
		Apps:        []string{"web"},
		Domains:     []string{"*.preview.example"},
		Dry:         true,
	})
	if err != nil {
		t.Fatalf("EnsurePreviewAlias: %v", err)
	}
	return ensured.GetHostnames()
}

func TestADryEnsurePreviewAliasOfANewPreviewAnswersNoHostnameAndRecordsNothing(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	previewBootstrapped(t, client)

	if hostnames := dryEnsurePreviewAlias(t, client); len(hostnames) != 0 {
		t.Errorf("a dry EnsurePreviewAlias of a preview never deployed = %v, want no hostname: its alias is assigned on its first deploy, and one signed now would never exist", hostnames)
	}
	for _, key := range []keyvalue.Key{stackrecords.NewPreviewKeyRecordKey(), stackrecords.EnvironmentKey(environment.TierPreview, "shop", "pr-7")} {
		if _, err := vendor.KeyValues().Read(context.Background(), key); !errors.Is(err, keyvalue.ErrNotFound) {
			t.Errorf("reading %s after a dry run = %v, want nothing written", key, err)
		}
	}
}

func TestADryEnsurePreviewAliasOfADeployedPreviewAnswersTheAliasItIsServedOn(t *testing.T) {
	builtProject(t)
	client, _ := deployServed(t)
	previewBootstrapped(t, client)
	result, _ := deploy(t, client, previewRequest())
	if !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}

	if got, want := dryEnsurePreviewAlias(t, client)["web"], strings.TrimPrefix(servedURLs(result)[0], "https://"); got != want {
		t.Errorf("a dry EnsurePreviewAlias of a deployed preview answered %q, want %q, the alias it is served on", got, want)
	}
}

func TestADryDeployOfANewPreviewSaysItsHostnameIsAssignedOnItsFirstDeploy(t *testing.T) {
	builtProject(t)
	client, _ := deployServed(t)
	previewBootstrapped(t, client)
	req := previewRequest()
	req.Dry = true

	result, events := deploy(t, client, req)
	if !result.GetSuccess() {
		t.Fatalf("Deploy(dry) = %q", result.GetError())
	}
	said := false
	for _, event := range events {
		if strings.Contains(event.GetMessage(), "assigned on its first deploy") {
			said = true
		}
		if strings.Contains(event.GetMessage(), "pr-7-") {
			t.Errorf("the dry deploy said %q, want no hostname for a preview never deployed: one signed now would never exist", event.GetMessage())
		}
	}
	if !said {
		t.Error("the dry deploy of a new preview never said its hostname is assigned on its first deploy")
	}
}

func ensureAlias(t *testing.T, client contractv1connect.ProviderServiceClient, token string) {
	t.Helper()
	if _, err := client.EnsurePreviewAlias(context.Background(), &contractv1.EnsurePreviewAliasRequest{
		Slug:        "shop",
		Environment: previewRequest().GetEnvironment(),
		Token:       token,
		Apps:        []string{"web"},
		Domains:     []string{"*.preview.example"},
	}); err != nil {
		t.Fatalf("EnsurePreviewAlias: %v", err)
	}
}

func forgetAlias(t *testing.T, client contractv1connect.ProviderServiceClient, token string) {
	t.Helper()
	if _, err := client.ForgetPreviewAlias(context.Background(), &contractv1.ForgetPreviewAliasRequest{
		Slug:        "shop",
		Environment: previewRequest().GetEnvironment(),
		Token:       token,
	}); err != nil {
		t.Fatalf("ForgetPreviewAlias: %v", err)
	}
}

func isPreviewRecorded(t *testing.T, vendor *fake.Provider) bool {
	t.Helper()
	_, err := vendor.KeyValues().Read(context.Background(), stackrecords.EnvironmentKey(environment.TierPreview, "shop", "pr-7"))
	if err != nil && !errors.Is(err, keyvalue.ErrNotFound) {
		t.Fatal(err)
	}
	return err == nil
}

func TestAPreviewRefusedForAnOverlongLabelIsNeverRecorded(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	previewBootstrapped(t, client)
	if result := usePreviewWildcard(t, client, "preview.acme.com", edged(fake.KindRelay, "acme.com")); !result.GetSuccess() {
		t.Fatalf("UsePreviewWildcard() = %q", result.GetError())
	}
	slug := strings.Repeat("s", 45)
	recorded := func() bool {
		t.Helper()
		_, err := vendor.KeyValues().Read(context.Background(), stackrecords.EnvironmentKey(environment.TierPreview, slug, "pr-7"))
		if err != nil && !errors.Is(err, keyvalue.ErrNotFound) {
			t.Fatal(err)
		}
		return err == nil
	}

	req := previewDeployRequest()
	req.Manifest.Slug = slug
	if result, _, err := deployStream(t, client, req); err == nil && result.GetSuccess() {
		t.Fatal("Deploy() of a label over 63 characters succeeded")
	}
	if recorded() {
		t.Error("Deploy() refused the label but recorded the preview, want nothing for `ocel preview ls` to list")
	}

	if _, err := client.EnsurePreviewAlias(context.Background(), &contractv1.EnsurePreviewAliasRequest{
		Slug: slug, Environment: previewDeployRequest().GetEnvironment(), Token: "abcdefghijklmnop", Apps: []string{"web"},
	}); err == nil {
		t.Fatal("EnsurePreviewAlias() of a label over 63 characters succeeded")
	}
	if recorded() {
		t.Error("EnsurePreviewAlias() refused the label but recorded the preview, want nothing for `ocel preview ls` to list")
	}
}

func TestForgetPreviewAliasRemovesTheAliasANeverDeployedPreviewWasAssigned(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	previewBootstrapped(t, client)
	ensureAlias(t, client, "abcdefghijklmnop")

	forgetAlias(t, client, "abcdefghijklmnop")

	if isPreviewRecorded(t, vendor) {
		t.Error("pr-7 is still recorded after its alias was forgotten before any deploy claimed it, want nothing left for `ocel preview ls` to list")
	}
}

func TestForgetPreviewAliasKeepsAPreviewAnotherRunAssignedOrADeployClaimed(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	previewBootstrapped(t, client)
	ensureAlias(t, client, "abcdefghijklmnop")

	forgetAlias(t, client, "qrstuvwxyzabcdef")
	if !isPreviewRecorded(t, vendor) {
		t.Fatal("forgetting an alias token pr-7 was never assigned removed its record, want it kept for the run that was")
	}

	if result, _ := deploy(t, client, previewRequest()); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}
	listed, err := client.ListEnvironments(context.Background(), &contractv1.ListEnvironmentsRequest{Slug: "shop"})
	if err != nil {
		t.Fatal(err)
	}
	token := strings.TrimPrefix(listed.GetEnvironments()[0].GetAliasUrls()[0], "https://pr-7-")[:edge.PreviewTokenLen]
	forgetAlias(t, client, token)
	if !isPreviewRecorded(t, vendor) {
		t.Error("forgetting the alias of a deployed preview removed its record, want a preview a deploy claimed kept until `ocel preview rm`")
	}
}

func TestEnsurePreviewAliasAnswersTheHostnamesThePreviewDeployIsServedOn(t *testing.T) {
	builtProject(t)
	client, _ := deployServed(t)
	previewBootstrapped(t, client)

	ensured, err := client.EnsurePreviewAlias(context.Background(), &contractv1.EnsurePreviewAliasRequest{
		Slug:        "shop",
		Environment: previewRequest().GetEnvironment(),
		Token:       "abcdefghijklmnop",
		Apps:        []string{"web"},
		Domains:     []string{"*.preview.example"},
	})
	if err != nil {
		t.Fatalf("EnsurePreviewAlias: %v", err)
	}
	hostname := ensured.GetHostnames()["web"]
	if !strings.HasPrefix(hostname, "pr-7-abcdefghijklmnop") {
		t.Fatalf("EnsurePreviewAlias = %v, want web on pr-7 and the token the CLI minted", ensured.GetHostnames())
	}

	result, _ := deploy(t, client, previewRequest())
	if !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}
	if want := []string{"https://" + hostname}; !slices.Equal(servedURLs(result), want) {
		t.Errorf("the deploy announced %v, want %v: the build baked in the alias it was answered", servedURLs(result), want)
	}

	again, err := client.EnsurePreviewAlias(context.Background(), &contractv1.EnsurePreviewAliasRequest{
		Slug:        "shop",
		Environment: previewRequest().GetEnvironment(),
		Token:       "qrstuvwxyz234567",
		Apps:        []string{"web"},
		Domains:     []string{"*.preview.example"},
	})
	if err != nil {
		t.Fatalf("EnsurePreviewAlias again: %v", err)
	}
	if again.GetHostnames()["web"] != hostname {
		t.Errorf("EnsurePreviewAlias again = %v, want the alias the preview already has", again.GetHostnames())
	}
	if again.GetToken() != "abcdefghijklmnop" {
		t.Errorf("EnsurePreviewAlias again answered the token %q, want abcdefghijklmnop, the one the preview was created with, for its deploy to carry", again.GetToken())
	}
}

func deployBuiltFor(t *testing.T, client contractv1connect.ProviderServiceClient, token string) *progressv1.OperationResult {
	t.Helper()
	req := previewRequest()
	req.AliasToken = token
	result, _ := deploy(t, client, req)
	return result
}

func TestAPreviewDeployRecreatesTheAliasItsBuildWasGivenWhenAFailedRunForgotIt(t *testing.T) {
	builtProject(t)
	client, _ := deployServed(t)
	previewBootstrapped(t, client)
	ensureAlias(t, client, "abcdefghijklmnop")
	forgetAlias(t, client, "abcdefghijklmnop")

	result := deployBuiltFor(t, client, "abcdefghijklmnop")

	if !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}
	if urls := servedURLs(result); len(urls) != 1 || !strings.HasPrefix(urls[0], "https://pr-7-abcdefghijklmnop") {
		t.Errorf("the deploy is served on %v, want the alias abcdefghijklmnop its build baked in", urls)
	}
}

func TestAPreviewKeepsTheAliasItsBuildWasGivenWhenAFailedRunForgetsItBeforeTheDeployClaimsThePreview(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	previewBootstrapped(t, client)
	ensureAlias(t, client, "abcdefghijklmnop")
	vendor.WithHooks(func(hooks *provider.Hooks) {
		hooks.PreflightDeploy = func(context.Context, provider.DeployPreflight) error {
			_, err := client.ForgetPreviewAlias(context.Background(), &contractv1.ForgetPreviewAliasRequest{
				Slug: "shop", Environment: previewRequest().GetEnvironment(), Token: "abcdefghijklmnop",
			})
			return err
		}
	})

	if result := deployBuiltFor(t, client, "abcdefghijklmnop"); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}
	vendor.WithHooks(func(hooks *provider.Hooks) { hooks.PreflightDeploy = nil })
	next := deployBuiltFor(t, client, "")

	if !next.GetSuccess() {
		t.Fatalf("the next Deploy() = %q", next.GetError())
	}
	if urls := servedURLs(next); len(urls) != 1 || !strings.HasPrefix(urls[0], "https://pr-7-abcdefghijklmnop") {
		t.Errorf("the next deploy is served on %v, want the alias abcdefghijklmnop the first deploy was served on", urls)
	}
	meta, err := stackrecords.ReadEnvironmentMeta(context.Background(), vendor.KeyValues(), environment.TierPreview, "shop", "pr-7")
	if err != nil {
		t.Fatal(err)
	}
	if meta.AliasToken != "abcdefghijklmnop" {
		t.Errorf("pr-7 records the alias token %q, want abcdefghijklmnop, the one its deploys were served on", meta.AliasToken)
	}
	for _, host := range meta.Aliases {
		if !strings.Contains(host.Hostname, "abcdefghijklmnop") {
			t.Errorf("pr-7 records the alias %s, want every alias on the token abcdefghijklmnop", host.Hostname)
		}
	}
}

func TestAPreviewDeployRecreatesTheAliasItsBuildWasGivenWhenThePreviewWasRemovedMeanwhile(t *testing.T) {
	builtProject(t)
	client, _ := deployServed(t)
	previewBootstrapped(t, client)
	if result := deployBuiltFor(t, client, ""); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}
	ensureAlias(t, client, "abcdefghijklmnop")
	built := readListedAliasToken(t, client)
	removing, err := client.RemoveEnvironment(context.Background(), &contractv1.RemoveEnvironmentRequest{
		Slug: "shop", Environment: &environmentv1.Environment{
			Tier: environmentv1.Tier_TIER_PREVIEW, Identity: "pr-7", Lifecycle: environmentv1.Lifecycle_LIFECYCLE_PERSISTENT,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result, err := drain(removing); err != nil || !result.GetSuccess() {
		t.Fatalf("RemoveEnvironment() = %q, %v", result.GetError(), err)
	}

	result := deployBuiltFor(t, client, built)

	if !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}
	if urls := servedURLs(result); len(urls) != 1 || !strings.HasPrefix(urls[0], "https://pr-7-"+built) {
		t.Errorf("the deploy is served on %v, want the alias %s its build baked in", urls, built)
	}
}

func readListedAliasToken(t *testing.T, client contractv1connect.ProviderServiceClient) string {
	t.Helper()
	listed, err := client.ListEnvironments(context.Background(), &contractv1.ListEnvironmentsRequest{Slug: "shop"})
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimPrefix(listed.GetEnvironments()[0].GetAliasUrls()[0], "https://pr-7-")[:edge.PreviewTokenLen]
}

func TestAPreviewDeployIsRefusedWhenThePreviewWasGivenAnotherAliasSinceItsBuild(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	plane := vendor.Routers().(*fake.Routers).DataPlane(fake.RouterRelay)
	previewBootstrapped(t, client)
	ensureAlias(t, client, "abcdefghijklmnop")
	forgetAlias(t, client, "abcdefghijklmnop")
	ensureAlias(t, client, "qrstuvwxyz234567")

	result := deployBuiltFor(t, client, "abcdefghijklmnop")

	if result.GetSuccess() || !strings.Contains(result.GetError(), "another alias") {
		t.Errorf("Deploy() = %v %q, want it refused: its build baked the alias abcdefghijklmnop and the preview is now on qrstuvwxyz234567", result.GetSuccess(), result.GetError())
	}
	if served := plane.ListServedHostnames(); len(served) != 0 {
		t.Errorf("%v are served, want nothing: a build is never served on hostnames it was not built for", served)
	}
}

func TestDeployAnnouncesThePreviewHostnameOfTheGlobalWildcard(t *testing.T) {
	builtProject(t)
	client, _ := deployServed(t)
	previewBootstrapped(t, client)
	if result := usePreviewWildcard(t, client, "preview.acme.com", edged(fake.KindRelay, "acme.com")); !result.GetSuccess() {
		t.Fatalf("UsePreviewWildcard() = %q", result.GetError())
	}

	result, _ := deploy(t, client, previewDeployRequest())
	if !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}
	urls := servedURLs(result)
	if len(urls) != 1 || !regexp.MustCompile(`^https://shop-[a-z2-7]{24}\.preview\.acme\.com$`).MatchString(urls[0]) {
		t.Errorf("the preview deploy announced %v, want the project's slug and a signed token on the global wildcard, and never the preview's name", urls)
	}
}

func TestDeployAnnouncesAPreviewHostnamePerAppWhenTheProjectHasMoreThanOne(t *testing.T) {
	builtProject(t)
	client, _ := deployServed(t)
	previewBootstrapped(t, client)
	if result := usePreviewWildcard(t, client, "preview.acme.com", edged(fake.KindRelay, "acme.com")); !result.GetSuccess() {
		t.Fatalf("UsePreviewWildcard() = %q", result.GetError())
	}

	req := twoAppRequest()
	req.Manifest.Domains = nil
	req.Environment = &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PREVIEW, Identity: "pr-7"}
	req.Edge = &contractv1.EdgeSelection{Kind: string(fake.KindRelay)}

	result, _ := deploy(t, client, req)
	if !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}
	web, admin := servedAppURLs(result, "web"), servedAppURLs(result, "admin")
	if len(web) != 1 || len(admin) != 1 || web[0] == admin[0] {
		t.Errorf("the preview deploy announced %v for web and %v for admin, want one hostname per app: the appless hostname is ambiguous once a project has two apps",
			web, admin)
	}
}

func TestDeployRefusesAProductionProjectThatDeclaresNoHostname(t *testing.T) {
	builtProject(t)
	client, _ := deployServed(t)

	req := deployRequest()
	req.Manifest.Domains = nil

	stream, err := client.Deploy(context.Background(), req)
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

	if !strings.Contains(said, "domains.production") {
		t.Fatalf("Deploy() of a project declaring no hostname = %q, want it refused for the domain it does not declare", said)
	}
}

func TestDeployServesAHostnameDeclaredOnAnAppRatherThanTheProject(t *testing.T) {
	builtProject(t)
	client, _ := deployServed(t)

	req := deployRequest()
	req.Manifest.Domains = nil
	req.Manifest.Apps[0].Domains = []*contractv1.TierDomains{{
		Tier:      environmentv1.Tier_TIER_PRODUCTION,
		Hostnames: []string{"shop.example"},
	}}

	req.Edge = writtenBy("shop.example")

	result, _ := deploy(t, client, req)
	if !result.GetSuccess() {
		t.Fatalf("Deploy() of a project whose only hostname sits on an app = %q, want it admitted", result.GetError())
	}
	if !slices.Equal(servedURLs(result), []string{"https://shop.example"}) {
		t.Errorf("the deploy returned urls %v, want the app-declared hostname it attached printed", servedURLs(result))
	}
}

func TestDeployAnnouncesEachAppsOwnHostnameUnderThatApp(t *testing.T) {
	builtProject(t)
	client, _ := deployServed(t)

	req := twoAppRequest()
	req.Manifest.Domains = nil
	req.Manifest.Apps[0].Domains = []*contractv1.TierDomains{{
		Tier:      environmentv1.Tier_TIER_PRODUCTION,
		Hostnames: []string{"shop.example"},
	}}
	req.Manifest.Apps[1].Domains = []*contractv1.TierDomains{{
		Tier:      environmentv1.Tier_TIER_PRODUCTION,
		Hostnames: []string{"admin.shop.example"},
	}}
	req.Edge = writtenBy("shop.example")

	result, _ := deploy(t, client, req)
	if !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}
	if got := servedAppURLs(result, "web"); !slices.Equal(got, []string{"https://shop.example"}) {
		t.Errorf("web has %v, want the hostname web itself declares", got)
	}
	if got := servedAppURLs(result, "admin"); !slices.Equal(got, []string{"https://admin.shop.example"}) {
		t.Errorf("admin has %v, want the hostname admin itself declares", got)
	}
}

type defaultingTo struct {
	*fake.Provider
	kind edge.Kind
}

func (d defaultingTo) Facts() provider.Facts {
	facts := d.Provider.Facts()
	facts.DefaultEdge = d.kind
	return facts
}

func TestADeployNamingNoEdgeGoesToTheEdgeTheProvidersFactsDefaultTo(t *testing.T) {
	builtProject(t)
	vendor := defaultingTo{Provider: fake.NewProvider(fake.Options{}), kind: fake.KindDirect}
	client := servedBy(t, vendor)

	req := deployRequest()
	req.Edge = writtenBy("shop.example")
	if result, _ := deploy(t, client, req); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}
	fronts := vendor.DNS().(*fake.DNS).Fronts()
	if len(fronts) == 0 || slices.ContainsFunc(fronts, func(front edge.Kind) bool { return front != fake.KindDirect }) {
		t.Errorf("the deploy opened its DNS under %v, want the %s edge Facts().DefaultEdge names", fronts, fake.KindDirect)
	}
}

func noteOf(result *progressv1.OperationResult) string {
	return strings.Join(result.GetUrlNotes(), "\n")
}

func TestADeployOpensItsDNSForTheEdgeTheProviderDefaultsTo(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)

	req := deployRequest()
	req.Edge = writtenBy("shop.example")
	if result, _ := deploy(t, client, req); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}
	fronts := vendor.DNS().(*fake.DNS).Fronts()
	if len(fronts) == 0 || slices.ContainsFunc(fronts, func(front edge.Kind) bool { return front != fake.KindRelay }) {
		t.Errorf("the deploy opened its DNS under %v, want the %s edge the provider defaults to", fronts, fake.KindRelay)
	}
}

func TestADeployPassesOnAWarningTheEdgeRaisesWhileItReconciles(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	vendor.Edges().(*fake.Edges).Edge(fake.KindRelay).WarnOnReconcile("shop.a.acme.com is more than one label below acme.com")

	req := deployRequest()
	req.Edge = &contractv1.EdgeSelection{Kind: string(fake.KindRelay)}

	result, events := deploy(t, client, req)
	if !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want a warning to leave the deploy standing", result.GetError())
	}
	if !slices.ContainsFunc(events, func(event *progressv1.OperationEvent) bool {
		return event.GetLevel() == progressv1.Level_LEVEL_WARN && strings.Contains(event.GetMessage(), "more than one label below")
	}) {
		t.Error("the deploy dropped the warning the edge raised while it reconciled")
	}
}

func removePersistentPr7(t *testing.T, client contractv1connect.ProviderServiceClient) *progressv1.OperationResult {
	t.Helper()
	removing, err := client.RemoveEnvironment(context.Background(), &contractv1.RemoveEnvironmentRequest{
		Slug: "shop", Environment: &environmentv1.Environment{
			Tier: environmentv1.Tier_TIER_PREVIEW, Identity: "pr-7", Lifecycle: environmentv1.Lifecycle_LIFECYCLE_PERSISTENT,
		},
	})
	if err != nil {
		t.Error(err)
		return nil
	}
	removed, err := drain(removing)
	if err != nil {
		t.Error(err)
	}
	return removed
}

func TestRemovingAPreviewWhileItsDeployPromotesIsRefusedAndTheDeployLands(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	previewBootstrapped(t, client)
	if first, _ := deploy(t, client, previewRequest()); !first.GetSuccess() {
		t.Fatalf("Deploy() = %q", first.GetError())
	}
	plane := vendor.Routers().(*fake.Routers).DataPlane(fake.RouterRelay)

	var removed *progressv1.OperationResult
	plane.BeforeNextPointerMove(func() {
		plane.BeforeNextPointerMove(func() {
			removed = removePersistentPr7(t, client)
		})
	})
	result, _ := deploy(t, client, previewRequest())

	if removed.GetSuccess() || !strings.Contains(removed.GetError(), "a deploy to pr-7 is running: remove it again once it ends") {
		t.Errorf("RemoveEnvironment() between the promote and the deployment hostname's move = %q, want it refused while the deploy holds pr-7", removed.GetError())
	}
	if !result.GetSuccess() {
		t.Errorf("Deploy() = %q, want it to land: the removal never ran", result.GetError())
	}
}

func TestRemovingAPreviewWhileItsDeployProvisionsIsRefusedAndTheDeployLands(t *testing.T) {
	for name, existing := range map[string]bool{"on its first deploy": false, "on a deploy of an existing preview": true} {
		t.Run(name, func(t *testing.T) {
			builtProject(t)
			client, vendor := deployServed(t)
			previewBootstrapped(t, client)
			if existing {
				if first, _ := deploy(t, client, previewRequest()); !first.GetSuccess() {
					t.Fatalf("Deploy() = %q", first.GetError())
				}
			}
			var removed *progressv1.OperationResult
			vendor.FakeStacks().Entering(func(spec provider.StackSpec) error {
				if spec.App != nil && removed == nil {
					removed = removePersistentPr7(t, client)
				}
				return nil
			})

			result, _ := deploy(t, client, previewRequest())

			if removed.GetSuccess() || !strings.Contains(removed.GetError(), "a deploy to pr-7 is running") {
				t.Errorf("RemoveEnvironment() while pr-7 provisioned = %q, want it refused while the deploy holds pr-7", removed.GetError())
			}
			if !result.GetSuccess() {
				t.Errorf("Deploy() = %q, want it to land: the removal never ran", result.GetError())
			}
			if !isPreviewRecorded(t, vendor) {
				t.Error("pr-7 is not recorded after its deploy landed")
			}
		})
	}
}

func TestAPreviewOnARouterThatAddressesItselfDeploysWithNoPreviewDomainAndAnnouncesItsOwnURL(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	previewBootstrapped(t, client)
	vendor.Edges().(*fake.Edges).Edge(fake.KindRelay).AddressesItself(true)

	result, _ := deploy(t, client, previewDeployRequest())
	if !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want the preview released on the address its router gives it", result.GetError())
	}
	urls := servedURLs(result)
	if len(urls) != 1 || !strings.HasSuffix(urls[0], ".fake.invalid") {
		t.Errorf("the preview deploy announced %v, want the one url its release recorded: the router answers on it and no hostname is bound", urls)
	}
}

func TestAProductionReleaseOnARouterThatAddressesItselfAnnouncesItsOwnURL(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	vendor.Edges().(*fake.Edges).Edge(fake.KindRelay).AddressesItself(true)
	req := deployRequest()
	req.Manifest.Domains = nil
	req.Edge = edged(fake.KindRelay, "")

	result, _ := deploy(t, client, req)
	if !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}
	if urls := servedURLs(result); len(urls) != 1 || !strings.HasSuffix(urls[0], ".fake.invalid") {
		t.Errorf("the deploy announced %v, want the one url its release recorded", urls)
	}
}

func TestAPreviewOnARouterThatAddressesItselfServesEachDeploymentOnItsOwnURLUntilAPruneDropsIt(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	previewBootstrapped(t, client)
	vendor.Edges().(*fake.Edges).Edge(fake.KindRelay).AddressesItself(true)
	host := func(url string) string { return strings.TrimPrefix(url, "https://") }

	first, _ := deploy(t, client, previewDeployRequest())
	deploy(t, client, previewDeployRequest())
	second, _ := deploy(t, client, previewDeployRequest())
	if !first.GetSuccess() || !second.GetSuccess() {
		t.Fatalf("Deploy() = %q, %q", first.GetError(), second.GetError())
	}
	firstDeployment, secondDeployment := findDeploymentURL(first, "web"), findDeploymentURL(second, "web")
	if !strings.Contains(firstDeployment, fake.DeploymentURLMarker) || firstDeployment == secondDeployment {
		t.Fatalf("the deploys announced deployment urls %q and %q, want each deploy its own url", firstDeployment, secondDeployment)
	}
	plane := vendor.Routers().(*fake.Routers).DataPlane(fake.RouterRelay)
	want := router.FormatDeploymentPointer("pr-7", first.GetPromotionId())
	if pointer, builds := plane.FindServingPointer(host(firstDeployment)); pointer != want || builds["web"] == "" {
		t.Errorf("the first deployment's url is served by %q with %v after the next deploy, want %s still serving it", pointer, builds, want)
	}

	pruning, err := client.RemoveStalePromotions(context.Background(), &contractv1.RemoveStalePromotionsRequest{
		Slug: "shop", KeepN: 1, Environment: previewDeployRequest().GetEnvironment(), Edge: previewDeployRequest().GetEdge(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result, err := drain(pruning); err != nil || !result.GetSuccess() {
		t.Fatalf("RemoveStalePromotions() = %q, %v", result.GetError(), err)
	}
	if pointer, _ := plane.FindServingPointer(host(firstDeployment)); pointer != "" {
		t.Errorf("the pruned deployment's url is still served by %q, want nothing", pointer)
	}
	if pointer, _ := plane.FindServingPointer(host(secondDeployment)); pointer == "" {
		t.Errorf("%s stopped serving, want the deployment the prune kept still served", secondDeployment)
	}
}

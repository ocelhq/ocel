package providerserver_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/constants"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	envvarsv1 "github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1/envvarsv1connect"
	"github.com/ocelhq/ocel/pkg/providerkit/appbuild"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/providerserver"
	"github.com/ocelhq/ocel/pkg/providerkit/records"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	"github.com/ocelhq/ocel/pkg/providerkit/resources"
	"github.com/ocelhq/ocel/pkg/providerkit/stackrecords"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const webDeploymentID = "0123456789abcdef0123456789abcdef"

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
	for _, app := range apps {
		built := filepath.Join(root, constants.ProjectStateDirName, "output", filepath.FromSlash(appArtifactPath(app)))
		if err := os.MkdirAll(built, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(built, builtEntrypoint), []byte("a built function"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(root)
}

func deployRequest() *contractv1.DeployRequest {
	return &contractv1.DeployRequest{
		Manifest: &contractv1.Manifest{
			SchemaVersion: "1",
			Slug:          "shop",
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
				Name:         "web",
				Framework:    &contractv1.Framework{Name: "next"},
				Compute:      string(provider.ComputeServerless),
				DeploymentId: webDeploymentID,
			}},
			Functions: []*contractv1.ManifestFunction{{
				LogicalName:  "server",
				App:          "web",
				Framework:    &contractv1.Framework{Name: "next"},
				Handler:      "index.handler",
				ArtifactPath: artifactPath,
			}},
		},
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION},
	}
}

func deploy(t *testing.T, client contractv1connect.ProviderServiceClient, req *contractv1.DeployRequest) (*progressv1.ResultEvent, []*progressv1.OperationEvent) {
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
) (*progressv1.ResultEvent, []*progressv1.OperationEvent, error) {
	t.Helper()
	stream, err := client.Deploy(context.Background(), req)
	if err != nil {
		t.Fatalf("Deploy() error = %v", err)
	}
	defer stream.Close()

	var events []*progressv1.OperationEvent
	var result *progressv1.ResultEvent
	for stream.Receive() {
		event := stream.Msg()
		events = append(events, event)
		if held := event.GetResult(); held != nil {
			result = held
		}
	}
	return result, events, stream.Err()
}

func TestDeployStandsUpInfraThenAppsAndPromotes(t *testing.T) {
	builtProject(t)
	client, p := deployServed(t)

	result, events := deploy(t, client, deployRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}
	if result.GetPromotionId() == "" {
		t.Error("Deploy() promoted nothing: the result names no promotion, so nothing can be rolled back to")
	}
	if len(result.GetBindings()) != 1 || result.GetBindings()[0].GetName() != "orders" {
		names := make([]string, 0, len(result.GetBindings()))
		for _, binding := range result.GetBindings() {
			names = append(names, binding.GetName())
		}
		t.Fatalf("Deploy() returned bindings %q, want only orders, the one the manifest declares", names)
	}
	if len(result.GetFunctions()) != 1 || result.GetFunctions()[0].GetUrl() == "" {
		t.Fatalf("Deploy() returned functions %v, want the one it stood up, carrying its url", result.GetFunctions())
	}

	if events[0].GetStagePlan() == nil {
		t.Fatalf("the first event is %T, want the stage plan: the CLI draws the tree before any work reports into it", events[0].GetEvent())
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
		t.Error("the app spec carries a function with no artifact, so the upload never reached the release")
	}
	if specs[1].App.Deployment != webDeploymentID {
		t.Errorf("the app spec names deployment %q, want %q: the router serves the build the CLI built under this id", specs[1].App.Deployment, webDeploymentID)
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
		t.Fatalf("Deploy() = %v, want it to name the wildcard the kit refuses", err)
	}
}

const adminDeploymentID = "fedcba9876543210fedcba9876543210"

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
		Name:         "admin",
		Framework:    &contractv1.Framework{Name: "next"},
		Compute:      string(provider.ComputeServerless),
		DeploymentId: adminDeploymentID,
	})
	manifest.Functions = append(manifest.Functions, &contractv1.ManifestFunction{
		LogicalName:  "admin-server",
		App:          "admin",
		Framework:    &contractv1.Framework{Name: "next"},
		Handler:      "index.handler",
		ArtifactPath: adminArtifactPath,
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
		t.Fatalf("the stacks port stood up %d apps, want web and admin", len(apps))
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

func TestDeployGrantsNothingToAnAppCarryingNoUsageEdge(t *testing.T) {
	builtProject(t)
	client, provider := deployServed(t)

	req := twoAppRequest()
	manifest := req.GetManifest()
	manifest.Usages = slices.DeleteFunc(manifest.Usages, func(usage *contractv1.ManifestUsage) bool {
		return usage.GetApp() == "admin"
	})
	if result, _ := deploy(t, client, req); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}

	for _, spec := range provider.FakeStacks().Provisioned() {
		if spec.App == nil || spec.App.App != "admin" {
			continue
		}
		if len(spec.App.Grants) != 0 || len(spec.App.Values.Bindings) != 0 {
			t.Errorf("admin is granted %v, want nothing for an app carrying no usage edge at all", spec.App.Grants)
		}
	}
}

func TestDeployRecordsEveryStackItStoodUp(t *testing.T) {
	builtProject(t)
	client, provider := deployServed(t)

	if result, _ := deploy(t, client, deployRequest()); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}

	entries, err := stackrecords.List(context.Background(), provider.Records(), edge.ClassProduction, "shop")
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
		t.Errorf("the infra stack records bindings %v, want the resource it stood up", infra.Bindings)
	}
	if app.App != "web" || app.Build == "" {
		t.Errorf("the app stack records %+v, want it named for the app and the build it serves", app.Stack)
	}
	if len(app.Functions) != 1 {
		t.Errorf("the app stack records %d functions, want the one it stood up", len(app.Functions))
	}
}

func TestDeployUploadsEveryFunctionArtifact(t *testing.T) {
	builtProject(t)
	client, provider := deployServed(t)

	if result, _ := deploy(t, client, deployRequest()); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}

	specs := provider.FakeStacks().Provisioned()
	ref := specs[1].App.Functions[0].Artifact
	opened, err := provider.Artifacts().Open(context.Background(), ref)
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

func (halfBindingStacks) Plan(ctx context.Context, spec provider.StackSpec, _ edge.Progress) (provider.Plan, error) {
	return resources.SynthesizedPlan(ctx, fake.NewArtifacts(), spec, provider.StackResult{})
}

func (halfBindingStacks) PlanDestroy(_ context.Context, ref provider.StackRef, _ edge.Progress) (provider.Plan, error) {
	return resources.SynthesizedRemoval(ref, provider.StackResult{}), nil
}

func (halfBindingStacks) Provision(_ context.Context, spec provider.StackSpec, _ edge.Progress) (provider.StackResult, error) {
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

func (halfBindingStacks) Destroy(context.Context, provider.StackRef, edge.Progress) error {
	return nil
}

func TestDeployRefusesABindingMissingAPropertyBeforeItRecordsIt(t *testing.T) {
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
		t.Fatalf("Deploy() with a Postgres binding carrying only a host = %v, want it refused as invalid", err)
	}
	if !strings.Contains(err.Error(), provider.PropertyPort) {
		t.Errorf("Deploy() failed with %q, want it to name the property that is missing", err)
	}
	if entries, rerr := stackrecords.List(context.Background(), base.Records(), edge.ClassProduction, "shop"); rerr != nil || len(entries) != 0 {
		t.Errorf("the refused deploy recorded %v, want nothing written for a binding the kit would not accept", entries)
	}
}

type countingCipher struct {
	records.Cipher

	mu     sync.Mutex
	opened int
}

func (c *countingCipher) Open(ctx context.Context, at records.SealScope, sealed []byte) ([]byte, error) {
	if at.Binding != "" {
		c.mu.Lock()
		c.opened++
		c.mu.Unlock()
	}
	return c.Cipher.Open(ctx, at, sealed)
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

func (s sealCounting) Cipher() records.Cipher { return s.cipher }

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

func (r *resolvingStacks) Plan(ctx context.Context, spec provider.StackSpec, progress edge.Progress) (provider.Plan, error) {
	return r.inner.Plan(ctx, spec, progress)
}

func (r *resolvingStacks) PlanDestroy(ctx context.Context, ref provider.StackRef, progress edge.Progress) (provider.Plan, error) {
	return r.inner.PlanDestroy(ctx, ref, progress)
}

func (r *resolvingStacks) Provision(ctx context.Context, spec provider.StackSpec, progress edge.Progress) (provider.StackResult, error) {
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

func (r *resolvingStacks) Destroy(ctx context.Context, ref provider.StackRef, progress edge.Progress) error {
	return r.inner.Destroy(ctx, ref, progress)
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
	if _, err := client.Configure(context.Background(), &contractv1.ConfigureRequest{}); err != nil {
		t.Fatalf("Configure() error = %v", err)
	}
	standsBootstrapped(t, client)
	return client
}

func deployServed(t *testing.T) (contractv1connect.ProviderServiceClient, *fake.Provider) {
	t.Helper()
	client, provider := contractServed(t, "1.0.0")
	standsBootstrapped(t, client)
	return client, provider
}

func standsBootstrapped(t *testing.T, client contractv1connect.ProviderServiceClient) {
	t.Helper()
	bootstrapOK(t, client, &contractv1.BootstrapRequest{
		Tier:     environmentv1.Tier_TIER_PRODUCTION,
		Features: []string{fake.FeatureCache, fake.FeatureImages},
	})
}

func declaresNeed(t *testing.T, app string, need edge.Need) {
	t.Helper()
	dir := appbuild.AppArtifactRoot(appbuild.ArtifactRoot(), app)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(edge.ServeDescriptor{
		Needs: map[edge.Need]edge.NeedDetail{need: {Routes: []string{"/feed"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, edge.ServeDescriptorFile), raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDeployWaivesANeedTheProjectAllowsToDegrade(t *testing.T) {
	builtProject(t)
	declaresNeed(t, "web", edge.NeedStreaming)
	client, provider := deployServed(t)
	provider.Edges().(*fake.Edges).Edge(fake.KindRelay).Serves(nil)

	req := deployRequest()
	req.Edge = &contractv1.EdgeSelection{
		Kind:          string(fake.KindRelay),
		AllowDegraded: []string{string(edge.NeedStreaming)},
	}

	result, events := deploy(t, client, req)
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() waiving %s = %q, want the deploy to stand up degraded", edge.NeedStreaming, result.GetError())
	}
	if !slices.ContainsFunc(events, func(event *progressv1.OperationEvent) bool {
		return event.GetDegraded().GetNeed() == string(edge.NeedStreaming)
	}) {
		t.Errorf("the deploy said nothing about %s, want the waived need reported out loud", edge.NeedStreaming)
	}
}

func TestDeployRefusesANeedTheProjectDoesNotWaive(t *testing.T) {
	builtProject(t)
	declaresNeed(t, "web", edge.NeedStreaming)
	client, provider := deployServed(t)
	provider.Edges().(*fake.Edges).Edge(fake.KindRelay).Serves(nil)

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

func operatorServed(t *testing.T) (contractv1connect.ProviderServiceClient, envvarsv1connect.EnvVarsServiceClient) {
	t.Helper()
	p := fake.NewProvider(fake.Options{})
	config := providerserver.Config{
		Version: "1.0.0",
		New:     func(context.Context, provider.Settings) (provider.Provider, error) { return p, nil },
	}
	server := httptest.NewServer(providerserver.ConformanceMux(config))
	t.Cleanup(server.Close)

	deploys := contractv1connect.NewProviderServiceClient(server.Client(), server.URL)
	if _, err := deploys.Configure(context.Background(), &contractv1.ConfigureRequest{}); err != nil {
		t.Fatalf("Configure() error = %v", err)
	}
	standsBootstrapped(t, deploys)
	return deploys, envvarsv1connect.NewEnvVarsServiceClient(server.Client(), server.URL)
}

func TestDeployPublishesBindingsWhereTheOperatorReadsThem(t *testing.T) {
	builtProject(t)
	deploys, vars := operatorServed(t)
	ctx := context.Background()

	if result, _ := deploy(t, deploys, deployRequest()); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}

	listed, err := vars.ListBindings(ctx, &envvarsv1.ListBindingsRequest{
		Slug: "shop",
		Tier: environmentv1.Tier_TIER_PRODUCTION,
	})
	if err != nil {
		t.Fatalf("ListBindings() = %v", err)
	}
	if len(listed.GetBindings()) != 1 || listed.GetBindings()[0].GetName() != "orders" {
		t.Fatalf("ListBindings() after a production deploy = %+v, want the binding the deploy published", listed.GetBindings())
	}

	removed, err := vars.RemoveBinding(ctx, &envvarsv1.RemoveBindingRequest{
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
	deploys, vars := operatorServed(t)
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

	listed, err := vars.ListBindings(ctx, &envvarsv1.ListBindingsRequest{
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
	deploys, vars := operatorServed(t)
	ctx := context.Background()

	if _, err := vars.SetBinding(ctx, &envvarsv1.SetBindingRequest{
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

	listed, err := vars.ListBindings(ctx, &envvarsv1.ListBindingsRequest{
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

func servedURLs(result *progressv1.ResultEvent) []string {
	var urls []string
	for _, app := range result.GetApps() {
		urls = append(urls, app.GetUrls()...)
	}
	return urls
}

func servedAppURLs(result *progressv1.ResultEvent, app string) []string {
	for _, held := range result.GetApps() {
		if held.GetApp() == app {
			return held.GetUrls()
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

func TestTheFirstDeploySettlesAHostnameItsDNSWriterPoints(t *testing.T) {
	builtProject(t)
	client, _ := deployServed(t)

	req := deployRequest()
	req.Edge = writtenBy("shop.example")
	result, _ := deploy(t, client, req)
	if !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}
	if !slices.Equal(servedURLs(result), []string{"https://shop.example"}) || noteOf(result) != "" {
		t.Errorf("the first deploy returned urls %v and the note %q, want the hostname it settled printed: no record was left to write by hand, so nothing waits on anyone",
			servedURLs(result), noteOf(result))
	}
}

func TestTheFirstDeploySettlesALocalhostNameWithNoDNSWriter(t *testing.T) {
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

func TestADeployBindsItsHostnamesOnlyOnceEveryStackItProvisionsStands(t *testing.T) {
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
		message := event.GetProgress().GetMessage()
		switch {
		case strings.HasPrefix(message, "Binding shop.example") && bound < 0:
			bound = len(said)
		case strings.HasPrefix(message, "provisioned "):
			provisioned = len(said)
		}
		if message != "" {
			said = append(said, message)
		}
	}
	if bound < 0 || provisioned < 0 || bound < provisioned {
		t.Errorf("the deploy said %v, want shop.example bound only after every stack was provisioned: an edge binds what already stands, such as the box's store name", said)
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
	client, provider := deployServed(t)

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
	edges := provider.Edges().(*fake.Edges)
	if bound := edges.Edge(fake.KindDirect).Bindings(); len(bound) != 0 {
		t.Errorf("the %s edge binds %v, want nothing: moving a hostname between edges is `ocel domain add`'s to order", fake.KindDirect, bound)
	}
	if bound := edges.Edge(fake.KindRelay).Bindings(); len(bound) != 1 || bound[0].Hostname != "shop.example" {
		t.Errorf("the %s edge binds %v, want shop.example still bound there: nothing moved it off", fake.KindRelay, bound)
	}
}

func TestALaterDeploySettlesAHostnameTheConfigNewlyDeclares(t *testing.T) {
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
		t.Fatalf("a deploy declaring one more hostname = %q, want it settled: the config is the declaration and the deploy reconciles it", result.GetError())
	}
	if want := []string{"https://shop.example", "https://www.shop.example"}; !slices.Equal(servedURLs(result), want) {
		t.Errorf("the deploy printed %v, want %v", servedURLs(result), want)
	}
}

func TestADeployWhoseDNSWriterFailsPromotesNothing(t *testing.T) {
	builtProject(t)
	client, provider := deployServed(t)
	writer, err := provider.DNS().Open(fake.KindZone, "shop.example", "")
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
	if _, promoted := spanStatuses(events)[promotionUnitSpan]; promoted {
		t.Error("the run promoted, want nothing promoted once settling a declared hostname failed")
	}
}

func TestADeployRefusedForAReasonNoWaitingFixesFailsWithThatReason(t *testing.T) {
	builtProject(t)
	client, provider := deployServed(t)
	reason := "no load balancer stands in this project for shop.example: run `ocel bootstrap`"
	provider.RefuseCertificates(refusal.Refuse(refusal.CodeNotReady, "%s", reason))

	req := deployRequest()
	req.Edge = writtenBy("shop.example")
	result, events := deploy(t, client, req)
	if result.GetSuccess() {
		t.Fatalf("Deploy() succeeded with the note %q, want it failed: a missing load balancer waits on `ocel bootstrap`, not on `ocel domain add`", noteOf(result))
	}
	if !strings.Contains(result.GetError(), reason) {
		t.Errorf("Deploy() = %q, want the refusal's own remedy", result.GetError())
	}
	if _, promoted := spanStatuses(events)[promotionUnitSpan]; promoted {
		t.Error("the run promoted, want nothing promoted over a hostname that could not be settled")
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
		t.Fatalf("Deploy() = %q, want it to succeed: an issuance still in flight holds back the hostname, not the release", result.GetError())
	}
	if !strings.Contains(noteOf(result), "the certificate is still validating") {
		t.Errorf("the note = %q, want it naming the issuance it waits on", noteOf(result))
	}
}

func TestADeployWhoseCertificateWaitsOnYouLeavesItToDomainAdd(t *testing.T) {
	builtProject(t)
	client, provider := deployServed(t)
	provider.RequireValidationRecords(edge.Record{Name: "_acme.shop.example", Type: edge.RecordTypeCNAME, Value: "validate.example"})

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
		for _, record := range event.GetDnsOwed().GetRecords() {
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
		t.Fatalf("Deploy() = %q, want it to succeed: a record only you can write holds back the hostname, not the release", result.GetError())
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
	want := "https://" + edge.ProjectPreview("preview.example").Host("pr-7", "")
	if !slices.Equal(servedURLs(result), []string{want}) {
		t.Errorf("the preview deploy announced %v, want %s: the project's own wildcard serves only this project, so no slug segment names it",
			servedURLs(result), want)
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
	want := "https://" + edge.SharedPreview("shop", "preview.acme.com").Host("pr-7", "")
	if !slices.Equal(servedURLs(result), []string{want}) {
		t.Errorf("the preview deploy announced %v, want %s: the project declares no domains.preview, so the global wildcard serves it",
			servedURLs(result), want)
	}
}

func TestAGlobalPreviewDeployOnAnEdgeThatRoutesByLabelHandsTheStacksTheLabelItsHostnameCarries(t *testing.T) {
	builtProject(t)
	client, provider := deployServed(t)
	previewBootstrapped(t, client)
	provider.Edges().(*fake.Edges).Edge(fake.KindRelay).RoutesPreviewsByLabel(true)
	if result := usePreviewWildcard(t, client, "preview.acme.com", edged(fake.KindRelay, "acme.com")); !result.GetSuccess() {
		t.Fatalf("UsePreviewWildcard() = %q", result.GetError())
	}

	result, _ := deploy(t, client, previewDeployRequest())
	if !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}

	want := edge.SharedPreview("shop", "preview.acme.com").Label("pr-7", "")
	for _, spec := range provider.FakeStacks().Provisioned() {
		if spec.App == nil {
			continue
		}
		if spec.App.PreviewLabel != want {
			t.Errorf("the spec for %s carries the preview label %q, want %q: an edge that routes the whole label to what it stood up "+
				"has to name it what the hostname says", spec.App.App, spec.App.PreviewLabel, want)
		}
	}
}

func TestAGlobalPreviewDeployOnAnEdgeThatDoesNotRouteByLabelHandsTheStacksNoLabel(t *testing.T) {
	builtProject(t)
	client, provider := deployServed(t)
	previewBootstrapped(t, client)
	if result := usePreviewWildcard(t, client, "preview.acme.com", edged(fake.KindRelay, "acme.com")); !result.GetSuccess() {
		t.Fatalf("UsePreviewWildcard() = %q", result.GetError())
	}

	result, _ := deploy(t, client, previewDeployRequest())
	if !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}

	for _, spec := range provider.FakeStacks().Provisioned() {
		if spec.App == nil {
			continue
		}
		if spec.App.PreviewLabel != "" {
			t.Errorf("the spec for %s carries the preview label %q, want none: this edge resolves a preview hostname itself, "+
				"so it imposes no name on what the provider stands up", spec.App.App, spec.App.PreviewLabel)
		}
	}
}

func TestAGlobalPreviewDeployLabelsEachAppWithTheFirstLabelOfTheHostnameItAnnounced(t *testing.T) {
	builtProject(t)
	client, provider := deployServed(t)
	previewBootstrapped(t, client)
	provider.Edges().(*fake.Edges).Edge(fake.KindRelay).RoutesPreviewsByLabel(true)
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

	labels := map[string]string{}
	for _, spec := range provider.FakeStacks().Provisioned() {
		if spec.App == nil {
			continue
		}
		labels[spec.App.App] = spec.App.PreviewLabel
	}
	for _, app := range []string{"web", "admin"} {
		urls := servedAppURLs(result, app)
		if len(urls) != 1 {
			t.Fatalf("the deploy announced %v for %s, want the one preview hostname it is served on", urls, app)
		}
		want, _, _ := strings.Cut(strings.TrimPrefix(urls[0], "https://"), ".")
		if labels[app] != want {
			t.Errorf("the spec for %s carries the preview label %q, and %s is the hostname announced: the label the edge hands over "+
				"is the first label of that hostname or the preview answers nothing", app, labels[app], urls[0])
		}
	}
}

func TestAPreviewDeployOnTheProjectsOwnWildcardHandsTheStacksNoLabel(t *testing.T) {
	builtProject(t)
	client, provider := deployServed(t)
	previewBootstrapped(t, client)

	result, _ := deploy(t, client, previewRequest())
	if !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}

	for _, spec := range provider.FakeStacks().Provisioned() {
		if spec.App == nil {
			continue
		}
		if spec.App.PreviewLabel != "" {
			t.Errorf("the spec for %s carries the preview label %q, want none: this project's own wildcard is answered per hostname, "+
				"so nothing reads a name out of the label", spec.App.App, spec.App.PreviewLabel)
		}
	}
}

func TestDeployAnnouncesAPreviewHostnamePerAppWhenTheProjectCarriesMoreThanOne(t *testing.T) {
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
	want := []string{
		"https://" + edge.SharedPreview("shop", "preview.acme.com").Host("pr-7", "web"),
		"https://" + edge.SharedPreview("shop", "preview.acme.com").Host("pr-7", "admin"),
	}
	if !slices.Equal(servedURLs(result), want) {
		t.Errorf("the preview deploy announced %v, want %v: the appless hostname is ambiguous once a project carries two apps",
			servedURLs(result), want)
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
		t.Errorf("the deploy returned urls %v, want the app-declared hostname it settled printed", servedURLs(result))
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
		t.Errorf("web carries %v, want the hostname web itself declares", got)
	}
	if got := servedAppURLs(result, "admin"); !slices.Equal(got, []string{"https://admin.shop.example"}) {
		t.Errorf("admin carries %v, want the hostname admin itself declares", got)
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
	provider := defaultingTo{Provider: fake.NewProvider(fake.Options{}), kind: fake.KindDirect}
	client := servedBy(t, provider)

	req := deployRequest()
	req.Edge = writtenBy("shop.example")
	if result, _ := deploy(t, client, req); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}
	fronts := provider.DNS().(*fake.DNS).Fronts()
	if len(fronts) == 0 || slices.ContainsFunc(fronts, func(front edge.Kind) bool { return front != fake.KindDirect }) {
		t.Errorf("the deploy opened its DNS under %v, want the %s edge Facts().DefaultEdge names", fronts, fake.KindDirect)
	}
}

func noteOf(result *progressv1.ResultEvent) string {
	return strings.Join(result.GetUrlNotes(), "\n")
}

func TestADeployOpensItsDNSForTheEdgeTheProviderDefaultsTo(t *testing.T) {
	builtProject(t)
	client, provider := deployServed(t)

	req := deployRequest()
	req.Edge = writtenBy("shop.example")
	if result, _ := deploy(t, client, req); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}
	fronts := provider.DNS().(*fake.DNS).Fronts()
	if len(fronts) == 0 || slices.ContainsFunc(fronts, func(front edge.Kind) bool { return front != fake.KindRelay }) {
		t.Errorf("the deploy opened its DNS under %v, want the %s edge the provider defaults to", fronts, fake.KindRelay)
	}
}

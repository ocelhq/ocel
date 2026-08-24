package providerkit_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	connect "connectrpc.com/connect"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	linksv1 "github.com/ocelhq/ocel/pkg/proto/common/links/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	envvarsv1 "github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1/envvarsv1connect"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const webDeploymentID = "0123456789abcdef0123456789abcdef"

const artifactPath = "apps/web/fn/server.zip"

func builtProject(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	built := filepath.Join(root, ".ocel/output", filepath.FromSlash(artifactPath))
	if err := os.MkdirAll(filepath.Dir(built), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(built, []byte("a built function"), 0o644); err != nil {
		t.Fatal(err)
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
					Type: linksv1.LinkType_LINK_TYPE_POSTGRES,
					Name: "orders",
				},
			}},
			Domains: []*contractv1.TierDomains{{
				Tier:      environmentv1.Tier_TIER_PRODUCTION,
				Hostnames: []string{"shop.example"},
			}},
			Apps: []*contractv1.ManifestApp{{
				Name:         "web",
				Framework:    "next",
				DeploymentId: webDeploymentID,
			}},
			Functions: []*contractv1.ManifestFunction{{
				LogicalName:  "server",
				App:          "web",
				Runtime:      "nodejs22.x",
				Handler:      "index.handler",
				ArtifactPath: artifactPath,
			}},
		},
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION},
	}
}

func deploy(t *testing.T, client contractv1connect.ProviderServiceClient, req *contractv1.DeployRequest) (*progressv1.ResultEvent, []*progressv1.OperationEvent) {
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
	if err := stream.Err(); err != nil {
		t.Fatalf("Deploy() stream error = %v", err)
	}
	return result, events
}

func TestDeployStandsUpInfraThenAppsAndPromotes(t *testing.T) {
	builtProject(t)
	client, provider := deployServed(t)

	result, events := deploy(t, client, deployRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}
	if result.GetPromotionId() == "" {
		t.Error("Deploy() promoted nothing: the result names no promotion, so nothing can be rolled back to")
	}
	if len(result.GetLinks()) != 1 || result.GetLinks()[0].GetName() != "orders" {
		t.Fatalf("Deploy() returned links %v, want the one the manifest declares", result.GetLinks())
	}
	if len(result.GetFunctions()) != 1 || result.GetFunctions()[0].GetUrl() == "" {
		t.Fatalf("Deploy() returned functions %v, want the one it stood up, carrying its url", result.GetFunctions())
	}

	if events[0].GetStagePlan() == nil {
		t.Fatalf("the first event is %T, want the stage plan: the CLI draws the tree before any work reports into it", events[0].GetEvent())
	}

	plans := provider.Releases().(*fake.Releaser).Plans()
	if len(plans) != 2 {
		t.Fatalf("the releaser saw %d plans, want the infra stack and the app stack", len(plans))
	}
	if plans[0].Kind != providerkit.StackInfra || plans[1].Kind != providerkit.StackApp {
		t.Fatalf("the releaser saw %s then %s, want infra before the apps that link to it", plans[0].Kind, plans[1].Kind)
	}
	if !slices.ContainsFunc(plans[1].App.Grants, func(link providerkit.Link) bool { return link.Name == "orders" }) {
		t.Errorf("the app plan grants %v, want the infra link the app binds a client to", plans[1].App.Grants)
	}
	if plans[1].App.Functions[0].Artifact.Key == "" {
		t.Error("the app plan carries a function with no artifact, so the upload never reached the release")
	}
	if plans[1].App.Deployment != webDeploymentID {
		t.Errorf("the app plan names deployment %q, want %q: the router serves the build the CLI built under this id", plans[1].App.Deployment, webDeploymentID)
	}
}

func TestDeployRecordsEveryStackItStoodUp(t *testing.T) {
	builtProject(t)
	client, provider := deployServed(t)

	if result, _ := deploy(t, client, deployRequest()); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}

	entries, err := providerkit.ReadStacks(context.Background(), provider.Records(), providerkit.ClassProduction, "shop")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("the project records %d stacks, want the infra stack and one app stack", len(entries))
	}
	var infra, app providerkit.StackEntry
	for _, entry := range entries {
		if entry.Name.IsInfra() {
			infra = entry
			continue
		}
		app = entry
	}
	if len(infra.Links) != 1 || infra.Links[0].Name != "orders" {
		t.Errorf("the infra stack records links %v, want the resource it stood up", infra.Links)
	}
	if app.App != "web" || app.Identity == "" {
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

	plans := provider.Releases().(*fake.Releaser).Plans()
	ref := plans[1].App.Functions[0].Artifact
	opened, err := provider.Artifacts().Open(context.Background(), ref)
	if err != nil {
		t.Fatalf("Open(%+v) after the deploy = %v, want the artifact stored where the plan named it", ref, err)
	}
	defer opened.Close()
	if !strings.HasPrefix(ref.Key, "prod/shop/web/") {
		t.Errorf("the artifact landed at %q, want it under the release's own prefix", ref.Key)
	}
}

func TestDeployPublishesEveryInfraLinkForItsAppsToRead(t *testing.T) {
	builtProject(t)
	client, provider := deployServed(t)

	if result, _ := deploy(t, client, deployRequest()); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}
	hostnameAdded(t, client)
	if result, _ := deploy(t, client, deployRequest()); !result.GetSuccess() {
		t.Fatalf("a second Deploy() = %q", result.GetError())
	}

	plans := provider.Releases().(*fake.Releaser).Plans()
	last := plans[len(plans)-1]
	if !slices.ContainsFunc(last.App.Grants, func(link providerkit.Link) bool {
		return link.Name == "orders" && link.Properties[providerkit.PropertyHost] != ""
	}) {
		t.Fatalf("a second deploy grants %v, want the published link read back whole", last.App.Grants)
	}
}

type refusingReleaser struct {
	*fake.Provider
	releaser providerkit.Releaser
}

func (r refusingReleaser) Releases() providerkit.Releaser { return r.releaser }

type halfLinkReleaser struct{}

func (halfLinkReleaser) Provision(_ context.Context, plan providerkit.StackPlan, _ providerkit.Reporter) (providerkit.StackResult, error) {
	var result providerkit.StackResult
	for _, resource := range plan.Resources {
		result.Links = append(result.Links, providerkit.Link{
			Type:       resource.Type,
			Name:       resource.Name,
			Properties: map[string]string{providerkit.PropertyHost: "db.invalid"},
		})
	}
	return result, nil
}

func (halfLinkReleaser) Destroy(context.Context, providerkit.StackRef, providerkit.Reporter) error {
	return nil
}

func TestDeployRefusesALinkMissingAPropertyBeforeItRecordsIt(t *testing.T) {
	builtProject(t)
	base := fake.NewProvider(fake.Options{})
	client := servedBy(t, refusingReleaser{Provider: base, releaser: halfLinkReleaser{}})

	stream, err := client.Deploy(context.Background(), deployRequest())
	if err != nil {
		t.Fatal(err)
	}
	for stream.Receive() {
	}
	err = stream.Err()
	stream.Close()

	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("Deploy() with a Postgres link carrying only a host = %v, want it refused as invalid", err)
	}
	if !strings.Contains(err.Error(), providerkit.PropertyPort) {
		t.Errorf("Deploy() failed with %q, want it to name the property that is missing", err)
	}
	if entries, rerr := providerkit.ReadStacks(context.Background(), base.Records(), providerkit.ClassProduction, "shop"); rerr != nil || len(entries) != 0 {
		t.Errorf("the refused deploy recorded %v, want nothing written for a link the kit would not accept", entries)
	}
}

func servedBy(t *testing.T, provider providerkit.Provider) contractv1connect.ProviderServiceClient {
	t.Helper()
	const token = "a-token"
	spec := providerkit.Spec{
		Version: "1.0.0",
		New:     func(context.Context, providerkit.Options) (providerkit.Provider, error) { return provider, nil },
	}
	server := httptest.NewServer(providerkit.NewMux(spec, token))
	t.Cleanup(server.Close)

	client := contractv1connect.NewProviderServiceClient(server.Client(), server.URL, connect.WithInterceptors(bearer(token)))
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
	dir := providerkit.AppArtifactRoot(providerkit.ArtifactRoot(), app)
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
	provider := fake.NewProvider(fake.Options{})
	spec := providerkit.Spec{
		Version: "1.0.0",
		New:     func(context.Context, providerkit.Options) (providerkit.Provider, error) { return provider, nil },
	}
	const token = "a-token"
	server := httptest.NewServer(providerkit.NewMux(spec, token))
	t.Cleanup(server.Close)

	auth := connect.WithInterceptors(bearer(token))
	deploys := contractv1connect.NewProviderServiceClient(server.Client(), server.URL, auth)
	if _, err := deploys.Configure(context.Background(), &contractv1.ConfigureRequest{}); err != nil {
		t.Fatalf("Configure() error = %v", err)
	}
	standsBootstrapped(t, deploys)
	return deploys, envvarsv1connect.NewEnvVarsServiceClient(server.Client(), server.URL, auth)
}

func TestDeployPublishesLinksWhereTheOperatorReadsThem(t *testing.T) {
	builtProject(t)
	deploys, vars := operatorServed(t)
	ctx := context.Background()

	if result, _ := deploy(t, deploys, deployRequest()); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}

	listed, err := vars.ListLinks(ctx, &envvarsv1.ListLinksRequest{
		Slug: "shop",
		Tier: environmentv1.Tier_TIER_PRODUCTION,
	})
	if err != nil {
		t.Fatalf("ListLinks() = %v", err)
	}
	if len(listed.GetLinks()) != 1 || listed.GetLinks()[0].GetName() != "orders" {
		t.Fatalf("ListLinks() after a production deploy = %+v, want the link the deploy published", listed.GetLinks())
	}

	removed, err := vars.RemoveLink(ctx, &envvarsv1.RemoveLinkRequest{
		Slug: "shop",
		Tier: environmentv1.Tier_TIER_PRODUCTION,
		Name: "orders",
	})
	if err != nil || !removed.GetRemoved() {
		t.Fatalf("RemoveLink() = %+v, %v, want the published link taken away", removed, err)
	}
}

func TestDeployRecordsTheFeaturesItsProjectDependsOn(t *testing.T) {
	builtProject(t)
	client, _ := deployServed(t)
	ctx := context.Background()

	if result, _ := deploy(t, client, deployRequest()); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}

	described, err := client.DescribeBootstrap(ctx, &contractv1.DescribeBootstrapRequest{
		Tier:           environmentv1.Tier_TIER_PRODUCTION,
		WithDependents: true,
	})
	if err != nil {
		t.Fatalf("DescribeBootstrap() error = %v", err)
	}
	for _, feature := range described.GetFeatures() {
		if feature.GetName() != fake.FeatureCache {
			continue
		}
		if !slices.Contains(feature.GetDependents(), "shop") {
			t.Errorf("%s reports dependents %v, want the project deployed against it", fake.FeatureCache, feature.GetDependents())
		}
	}

	stream, err := client.Bootstrap(ctx, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION})
	if err != nil {
		t.Fatal(err)
	}
	result, err := drain(stream)
	said := result.GetError() + connectMessage(err)
	if !strings.Contains(said, "shop") {
		t.Fatalf("dropping every feature = %q, want it refused for the project that depends on them", said)
	}
}

func hostnameAdded(t *testing.T, client contractv1connect.ProviderServiceClient) {
	t.Helper()
	stream, err := client.AddHostname(context.Background(), &contractv1.HostnameRequest{
		Slug:       "shop",
		Configured: []string{"shop.example"},
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

func TestDeployWithholdsTheURLUntilTheHostnameIsSettled(t *testing.T) {
	builtProject(t)
	client, _ := deployServed(t)

	result, _ := deploy(t, client, deployRequest())
	if !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}
	if len(result.GetAppUrls()) != 0 || !strings.Contains(result.GetUrlNote(), "shop.example") {
		t.Fatalf("the first deploy returned urls %v and the note %q, want no url and a note naming the hostname nothing is bound to yet",
			result.GetAppUrls(), result.GetUrlNote())
	}

	hostnameAdded(t, client)

	result, _ = deploy(t, client, deployRequest())
	if !result.GetSuccess() {
		t.Fatalf("a second Deploy() = %q", result.GetError())
	}
	if len(result.GetAppUrls()) == 0 || result.GetUrlNote() != "" {
		t.Errorf("the deploy after the hostname settled returned urls %v and the note %q, want the address printed",
			result.GetAppUrls(), result.GetUrlNote())
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

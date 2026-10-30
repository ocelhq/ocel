package providerserver_test

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"sync"
	"testing"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

func writeProject(t *testing.T, store records.Store, tier environment.Tier, slug string, project stackrecords.Project) {
	t.Helper()
	body, err := json.Marshal(project)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Write(context.Background(), records.Entry{Key: stackrecords.ProjectKey(tier, slug), Value: body}); err != nil {
		t.Fatal(err)
	}
}

func writeStack(t *testing.T, store records.Store, tier environment.Tier, slug string, name naming.StackName, stack stackrecords.Stack) {
	t.Helper()
	if err := stackrecords.Write(context.Background(), store, tier, slug, name, stack); err != nil {
		t.Fatal(err)
	}
}

func listProjects(t *testing.T, client contractv1connect.ProviderServiceClient) map[string]*contractv1.ProjectSummary {
	t.Helper()
	listed, err := client.ListProjects(context.Background(), &contractv1.ListProjectsRequest{})
	if err != nil {
		t.Fatalf("ListProjects() error = %v", err)
	}
	bySlug := map[string]*contractv1.ProjectSummary{}
	var order []string
	for _, project := range listed.GetProjects() {
		bySlug[project.GetSlug()] = project
		order = append(order, project.GetSlug())
	}
	if !slices.IsSorted(order) {
		t.Errorf("ListProjects() = %v, want the projects sorted by slug", order)
	}
	return bySlug
}

func describeProject(t *testing.T, client contractv1connect.ProviderServiceClient, slug string) *contractv1.DescribeProjectResponse {
	t.Helper()
	described, err := client.DescribeProject(context.Background(), &contractv1.DescribeProjectRequest{Slug: slug})
	if err != nil {
		t.Fatalf("DescribeProject(%s) error = %v", slug, err)
	}
	return described
}

var relayProject = stackrecords.Project{Edge: &stackrecords.ProjectEdge{Kind: fake.KindRelay}}

var (
	productionTier = environmentv1.Tier_TIER_PRODUCTION
	previewTier    = environmentv1.Tier_TIER_PREVIEW
)

func TestListProjectsNamesEveryProjectRecordedInEitherTier(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.0.0")
	store := vendor.KeyValues()
	writeProject(t, store, environment.TierProduction, "shop", stackrecords.Project{Edge: &stackrecords.ProjectEdge{Kind: fake.KindRelay}, UpdatedAt: 100})
	writeProject(t, store, environment.TierPreview, "shop", stackrecords.Project{Edge: &stackrecords.ProjectEdge{Kind: fake.KindDirect}, UpdatedAt: 300})
	writeProject(t, store, environment.TierPreview, "blog", stackrecords.Project{Edge: &stackrecords.ProjectEdge{Kind: fake.KindDirect}, UpdatedAt: 200})

	listed := listProjects(t, client)

	if got := slices.Sorted(maps.Keys(listed)); !slices.Equal(got, []string{"blog", "shop"}) {
		t.Fatalf("ListProjects() = %v, want every project recorded in either tier", got)
	}
	shop, blog := listed["shop"], listed["blog"]
	if !slices.Equal(shop.GetTiers(), []environmentv1.Tier{productionTier, previewTier}) || shop.GetEdge() != string(fake.KindRelay) || shop.GetUpdatedAt() != 300 {
		t.Errorf("shop = %v, want both tiers, the edge production is served by, and the latest deploy of either tier", shop)
	}
	if !slices.Equal(blog.GetTiers(), []environmentv1.Tier{previewTier}) || blog.GetEdge() != string(fake.KindDirect) || blog.GetUpdatedAt() != 200 {
		t.Errorf("blog = %v, want only the preview tier and its edge", blog)
	}
}

type previewUnbootstrappedStore struct{ records.Store }

var errPreviewUnbootstrapped = refusal.Refuse(refusal.CodeNotReady, "the preview tier is not bootstrapped")

func (s previewUnbootstrappedStore) Read(ctx context.Context, key records.Key) (records.Entry, error) {
	if key.Partition.Tier == environment.TierPreview {
		return records.Entry{}, errPreviewUnbootstrapped
	}
	return s.Store.Read(ctx, key)
}

func (s previewUnbootstrappedStore) List(ctx context.Context, in records.Partition, under ...string) ([]records.Entry, error) {
	if in.Tier == environment.TierPreview {
		return nil, errPreviewUnbootstrapped
	}
	return s.Store.List(ctx, in, under...)
}

type previewUnbootstrapped struct {
	*fake.Provider
	store records.Store
}

func (p previewUnbootstrapped) KeyValues() records.Store { return p.store }

func TestListProjectsOfAnAccountWithNoPreviewBootstrapListsItsProductionProjects(t *testing.T) {
	t.Parallel()
	vendor := fake.NewProvider(fake.Options{Region: "nowhere"})
	writeProject(t, vendor.KeyValues(), environment.TierProduction, "shop", relayProject)
	client := servedProvider(t, "1.0.0", previewUnbootstrapped{Provider: vendor, store: previewUnbootstrappedStore{Store: vendor.KeyValues()}})

	listed := listProjects(t, client)

	if shop, ok := listed["shop"]; !ok || len(listed) != 1 || !slices.Equal(shop.GetTiers(), []environmentv1.Tier{productionTier}) {
		t.Errorf("ListProjects() = %v, want shop in production alone: an account that never bootstrapped preview has no preview projects", listed)
	}
}

func TestListProjectsSaysWhichPromotionProductionServes(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.0.0")
	store := vendor.KeyValues()
	writeProject(t, store, environment.TierProduction, "shop", relayProject)
	writeProject(t, store, environment.TierPreview, "shop", relayProject)
	writeProject(t, store, environment.TierPreview, "blog", relayProject)
	seedPromotions(t, vendor, environment.TierProduction, "shop", "", "p1", "p2")
	release := naming.NewReleaseToken("b1", "")
	seedEnvironment(t, vendor, "shop", naming.AppStack("pr-7", "web", release), naming.AppStack("staging", "web", release))

	listed := listProjects(t, client)

	if shop := listed["shop"]; shop.GetProductionPromotionId() != "p2" || shop.GetPreviews() != 2 {
		t.Errorf("shop = %v, want production serving p2 beside its two previews", shop)
	}
	if blog := listed["blog"]; blog.GetProductionPromotionId() != "" || blog.GetPreviews() != 0 {
		t.Errorf("blog = %v, want no production promotion and no previews: it has neither", blog)
	}
}

func TestListProjectsLeavesOutAProjectItsRemovalForgot(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	if result, _ := deploy(t, client, deployRequest()); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}
	if err := stackrecords.RecordPartsUsed(context.Background(), vendor.KeyValues(), environment.TierProduction, "shop", []string{fake.PartCache}); err != nil {
		t.Fatal(err)
	}
	if _, listed := listProjects(t, client)["shop"]; !listed {
		t.Fatal("ListProjects() leaves out shop after its deploy, and a removal that drops it proves nothing")
	}

	if result := removeProject(t, client, projectRequest()); !result.GetSuccess() {
		t.Fatalf("RemoveProject() = %q", result.GetError())
	}

	if listed := listProjects(t, client); len(listed) > 0 {
		t.Errorf("ListProjects() = %v after shop was removed, want nothing", listed)
	}
}

func TestDescribeProjectListsEachEnvironmentWithThePromotionItServes(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.0.0")
	store := vendor.KeyValues()
	ctx := context.Background()
	writeProject(t, store, environment.TierProduction, "shop", relayProject)
	writeProject(t, store, environment.TierPreview, "shop", relayProject)
	writeStack(t, store, environment.TierProduction, "shop", naming.InfraStack(stackrecords.ProductionEnv), stackrecords.Stack{Kind: provider.StackInfra})
	seedStack(t, vendor, environment.TierProduction, "shop", stackrecords.EdgeState{
		Hosts: map[string]stackrecords.HostnameState{
			"shop.example":     {Pointer: router.DefaultPointer},
			"www.shop.example": {Pointer: router.DefaultPointer},
		},
	})
	seedPromotions(t, vendor, environment.TierProduction, "shop", "", "p1", "p2")
	seedEnvironment(t, vendor, "shop", naming.AppStack("pr-7", "web", releaseOf(t, releaseFor(0))))
	recordLabelledEnvironment(t, vendor, "pr-7", "pr-123", stackrecords.LifecyclePersistent)
	if _, _, err := stackrecords.RecordAliases(ctx, store, environment.TierPreview, "shop", "pr-7", recordedAliasToken,
		[]edge.PreviewHost{{Hostname: "web-pr-7.preview.acme.com", App: "web"}}); err != nil {
		t.Fatal(err)
	}
	seedPromotions(t, vendor, environment.TierPreview, "shop", "pr-7", "p9")

	described := describeProject(t, client, "shop")

	environments := described.GetEnvironments()
	if len(environments) != 2 {
		t.Fatalf("DescribeProject() names environments %v, want production and pr-7", environments)
	}
	prod, pr7 := environments[0], environments[1]
	if prod.GetEnvironment().GetTier() != productionTier || prod.GetPromotionId() != "p2" || prod.GetReleases()["web"] != releaseFor(1) ||
		!slices.Equal(prod.GetUrls(), []string{"https://shop.example", "https://www.shop.example"}) {
		t.Errorf("production = %v, want it serving p2's web release on both its hostnames", prod)
	}
	if pr7.GetEnvironment().GetTier() != previewTier || pr7.GetEnvironment().GetIdentity() != "pr-7" ||
		pr7.GetEnvironment().GetLifecycle() != environmentv1.Lifecycle_LIFECYCLE_PERSISTENT || pr7.GetEnvironment().GetLabel() != "pr-123" ||
		pr7.GetPromotionId() != "p9" || !slices.Equal(pr7.GetUrls(), []string{"https://web-pr-7.preview.acme.com"}) {
		t.Errorf("pr-7 = %v, want the persistent preview pr-123 serving p9 on its alias", pr7)
	}
	if described.GetProject().GetProductionPromotionId() != "p2" || described.GetProject().GetPreviews() != 1 {
		t.Errorf("DescribeProject() summarizes %v, want the same summary ListProjects gives", described.GetProject())
	}
}

func groupOf(groups []*contractv1.ResourceGroup, tier environmentv1.Tier, stack string) *contractv1.ResourceGroup {
	at := slices.IndexFunc(groups, func(group *contractv1.ResourceGroup) bool {
		return group.GetTier() == tier && group.GetStack() == stack
	})
	if at < 0 {
		return nil
	}
	return groups[at]
}

func resourceIDs(group *contractv1.ResourceGroup) []string {
	var ids []string
	for _, resource := range group.GetResources() {
		ids = append(ids, resource.GetKind()+" "+resource.GetId())
	}
	return ids
}

func TestDescribeProjectListsTheResourcesOfEveryStackItRecorded(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.0.0")
	store := vendor.KeyValues()
	writeProject(t, store, environment.TierProduction, "shop", relayProject)
	writeProject(t, store, environment.TierPreview, "shop", relayProject)
	infra := naming.InfraStack(stackrecords.ProductionEnv)
	web := naming.AppStack(stackrecords.ProductionEnv, "web", naming.NewReleaseToken("d1", "f1"))
	pr7 := naming.AppStack("pr-7", "web", naming.NewReleaseToken("d2", "f2"))
	writeStack(t, store, environment.TierProduction, "shop", infra, stackrecords.Stack{
		Kind:     provider.StackInfra,
		Bindings: []provider.Binding{{Type: provider.BindingPostgres, Name: "orders", Resource: "shop-orders"}},
	})
	writeStack(t, store, environment.TierProduction, "shop", web, stackrecords.Stack{
		Kind: provider.StackApp, App: "web", Functions: []provider.Function{{Name: "api", Physical: "shop-web-api"}},
	})
	writeStack(t, store, environment.TierPreview, "shop", pr7, stackrecords.Stack{
		Kind: provider.StackApp, App: "web", Functions: []provider.Function{{Name: "api", Physical: "shop-pr-7-api"}},
	})
	writeStack(t, store, environment.TierPreview, "shop", naming.InfraStack("pr-7"), stackrecords.Stack{Kind: provider.StackInfra})

	groups := describeProject(t, client, "shop").GetResources()

	for _, want := range []struct {
		tier  environmentv1.Tier
		stack naming.StackName
		ids   []string
	}{
		{productionTier, infra, []string{"fake:postgres shop-orders"}},
		{productionTier, web, []string{"fake:function shop-web-api"}},
		{previewTier, pr7, []string{"fake:function shop-pr-7-api"}},
	} {
		group := groupOf(groups, want.tier, want.stack.String())
		if group == nil {
			t.Errorf("DescribeProject() lists no resources of %s %s, want %v", want.tier, want.stack, want.ids)
			continue
		}
		if group.GetEnvironment() != want.stack.Env || !slices.Equal(resourceIDs(group), want.ids) {
			t.Errorf("DescribeProject() lists %s in %s as %v, want %v in it", want.stack, group.GetEnvironment(), resourceIDs(group), want.ids)
		}
	}
	if function := groupOf(groups, productionTier, web.String()).GetResources(); len(function) == 0 || function[0].GetConsoleUrl() != "https://console.fake.example/shop-web-api" {
		t.Errorf("DescribeProject() lists %v for web, want the link to its console the vendor gave", function)
	}
	if group := groupOf(groups, previewTier, naming.InfraStack("pr-7").String()); group != nil {
		t.Errorf("DescribeProject() lists %v for a stack that holds nothing, want no group for it", group)
	}
}

type listingStacks struct {
	provider.Stacks
	mu     sync.Mutex
	listed []provider.StackResources
}

func (s *listingStacks) Resources(ctx context.Context, req provider.StackResources) ([]provider.PhysicalResource, error) {
	s.mu.Lock()
	s.listed = append(s.listed, req)
	s.mu.Unlock()
	return s.Stacks.Resources(ctx, req)
}

func writeStackUpdatedAt(t *testing.T, store records.Store, name naming.StackName, updatedAt int64) {
	t.Helper()
	body, err := json.Marshal(stackrecords.Stack{Kind: provider.StackApp, App: name.App, Release: name.Release.String(), UpdatedAt: updatedAt})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Write(context.Background(), records.Entry{Key: stackrecords.StackKey(environment.TierProduction, "shop", name), Value: body}); err != nil {
		t.Fatal(err)
	}
}

func TestDescribeProjectTellsTheVendorWhichStackIsTheNewestReleaseOfItsApp(t *testing.T) {
	t.Parallel()
	base := fake.NewProvider(fake.Options{})
	stacks := &listingStacks{Stacks: base.Stacks()}
	client := servedBy(t, refusingStacks{Provider: base, stacks: stacks})
	store := base.KeyValues()
	writeProject(t, store, environment.TierProduction, "shop", relayProject)
	infra := naming.InfraStack(stackrecords.ProductionEnv)
	older := naming.AppStack(stackrecords.ProductionEnv, "web", naming.NewReleaseToken("d1", "f1"))
	newer := naming.AppStack(stackrecords.ProductionEnv, "web", naming.NewReleaseToken("d2", "f2"))
	writeStack(t, store, environment.TierProduction, "shop", infra, stackrecords.Stack{Kind: provider.StackInfra})
	writeStackUpdatedAt(t, store, older, 100)
	writeStackUpdatedAt(t, store, newer, 200)

	describeProject(t, client, "shop")

	newest := map[string]bool{}
	for _, req := range stacks.listed {
		newest[req.Ref.Name.String()] = req.NewestRelease
	}
	want := map[string]bool{infra.String(): false, older.String(): false, newer.String(): true}
	if !maps.Equal(newest, want) {
		t.Errorf("DescribeProject() listed stacks as newest release %v, want %v", newest, want)
	}
}

func TestDescribeProjectListsWhatTheVendorHoldsForTheProjectOutsideItsStacks(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.0.0")
	writeProject(t, vendor.KeyValues(), environment.TierProduction, "shop", relayProject)
	written := edge.Record{Name: "shop.example", Type: edge.RecordTypeCNAME, Value: "shop.fake.invalid"}
	seedStack(t, vendor, environment.TierProduction, "shop", stackrecords.EdgeState{
		Hosts: map[string]stackrecords.HostnameState{"shop.example": {Pointer: router.DefaultPointer, Written: []edge.Record{written}}},
	})

	group := groupOf(describeProject(t, client, "shop").GetResources(), productionTier, "")

	if group == nil || group.GetEnvironment() != "" || !slices.Equal(resourceIDs(group), []string{"fake:dns-record " + written.String()}) {
		t.Errorf("DescribeProject() lists %v outside shop's stacks, want the DNS record ocel wrote for it", resourceIDs(group))
	}
}

func TestDescribeProjectHandsTheVendorTheRoutersAndImagesTheTierRecords(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.0.0")
	var asked []provider.ProjectResourcesRequest
	vendor.WithHooks(func(hooks *provider.Hooks) {
		hooks.ListProjectResources = func(_ context.Context, req provider.ProjectResourcesRequest) ([]provider.PhysicalResource, error) {
			asked = append(asked, req)
			return nil, nil
		}
	})
	store := vendor.KeyValues()
	writeProject(t, store, environment.TierProduction, "shop", relayProject)
	routed := router.NewStackState(edge.StackState{Slug: "shop", Tier: environment.TierProduction, Bound: []string{"shop.example"}})
	seedStack(t, vendor, environment.TierProduction, "shop", stackrecords.EdgeState{Routers: map[router.Kind]router.StackState{"alb": routed}})
	writeStack(t, store, environment.TierProduction, "shop", naming.AppStack(stackrecords.ProductionEnv, "web", naming.NewReleaseToken("d1", "f1")), stackrecords.Stack{
		Kind: provider.StackApp, App: "web", Containers: []provider.AppContainer{{Name: "web", Image: "registry.example/shop.web:1"}},
	})

	describeProject(t, client, "shop")

	if len(asked) != 1 {
		t.Fatalf("DescribeProject() asked the vendor %d times, want once for production", len(asked))
	}
	if got := asked[0].Routers["alb"]; !slices.Equal(got.Edge.Bound, routed.Edge.Bound) {
		t.Errorf("ListProjectResources() was handed router %+v, want the alb router production records", got)
	}
	if !slices.Equal(asked[0].Images, []string{"registry.example/shop.web:1"}) {
		t.Errorf("ListProjectResources() was handed images %v, want the image web's stack records", asked[0].Images)
	}
}

func TestDescribeProjectNamesTheBootstrapPartsTheProjectShares(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.0.0")
	bootstrapped(t, vendor, environment.TierProduction, fake.PartCache)
	recordProject(t, vendor, "shop", fake.PartCache)
	recordProject(t, vendor, "blog")

	shared := func(slug string) []string {
		var names []string
		for _, part := range describeProject(t, client, slug).GetShared() {
			if part.GetTier() != productionTier {
				t.Errorf("DescribeProject(%s) shares %v, want only production parts: nothing else is bootstrapped", slug, part)
			}
			names = append(names, part.GetPart().GetName())
		}
		return names
	}

	if got := shared("shop"); !slices.Contains(got, fake.PartCache) || !slices.Contains(got, provider.BasePart) {
		t.Errorf("DescribeProject(shop) shares %v, want the base part and the cache it depends on", got)
	}
	if got := shared("blog"); slices.Contains(got, fake.PartCache) || !slices.Contains(got, provider.BasePart) {
		t.Errorf("DescribeProject(blog) shares %v, want the base part alone: blog never used the cache", got)
	}
}

func getProject(t *testing.T, client contractv1connect.ProviderServiceClient, slug string, tiers ...environmentv1.Tier) *contractv1.GetProjectResponse {
	t.Helper()
	if len(tiers) == 0 {
		tiers = []environmentv1.Tier{productionTier, previewTier}
	}
	got, err := client.GetProject(context.Background(), &contractv1.GetProjectRequest{Slug: slug, Tiers: tiers})
	if err != nil {
		t.Fatalf("GetProject(%s) error = %v", slug, err)
	}
	return got
}

func TestGetProjectReturnsItsTiersTheRecordedEdgeAndARegistryWithoutAPassword(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.0.0")
	writeProject(t, vendor.KeyValues(), environment.TierPreview, "shop", stackrecords.Project{
		Edge: &stackrecords.ProjectEdge{
			Kind:          fake.KindRelay,
			DNSKind:       string(fake.KindZone),
			DNSZone:       "shop.example",
			AllowDegraded: []string{"isr"},
			Options:       json.RawMessage(`{"tunnel":true}`),
		},
		Registry: &stackrecords.ProjectRegistry{Server: "ghcr.io", Namespace: "acme", Username: "acme-bot"},
	})

	got := getProject(t, client, "shop")

	if !slices.Equal(got.GetTiers(), []environmentv1.Tier{previewTier}) {
		t.Errorf("GetProject() tiers = %v, want preview alone", got.GetTiers())
	}
	selection := got.GetEdge()
	if selection.GetKind() != string(fake.KindRelay) || selection.GetDns().GetKind() != string(fake.KindZone) || selection.GetDns().GetZone() != "shop.example" ||
		!slices.Equal(selection.GetAllowDegraded(), []string{"isr"}) || selection.GetOptions().AsMap()["tunnel"] != true {
		t.Errorf("GetProject() names edge %v, want the one recorded", selection)
	}
	registry := got.GetRegistry()
	if registry.GetServer() != "ghcr.io" || registry.GetNamespace() != "acme" || registry.GetUsername() != "acme-bot" || registry.GetPassword() != "" {
		t.Errorf("GetProject() names registry %s/%s as %s (password set: %t), want ghcr.io/acme as acme-bot with no password",
			registry.GetServer(), registry.GetNamespace(), registry.GetUsername(), registry.GetPassword() != "")
	}
}

func TestGetProjectOfAProjectNeverDeployedNamesNoTier(t *testing.T) {
	t.Parallel()
	client, _ := contractServed(t, "1.0.0")

	if got := getProject(t, client, "shop"); len(got.GetTiers()) != 0 || got.GetEdge() != nil || got.GetRegistry() != nil {
		t.Errorf("GetProject() of a project never deployed names tiers %v, edge %t and registry %t, want no tier, edge or registry",
			got.GetTiers(), got.GetEdge() != nil, got.GetRegistry() != nil)
	}
}

func TestGetProjectReadsTheRecordOfNoOtherProject(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.0.0")
	writeProject(t, vendor.KeyValues(), environment.TierProduction, "shop", relayProject)
	unreadable := records.Entry{Key: stackrecords.ProjectKey(environment.TierProduction, "docs"), Value: []byte(`{"parts":"cache"}`)}
	if _, err := vendor.KeyValues().Write(context.Background(), unreadable); err != nil {
		t.Fatal(err)
	}

	if got := getProject(t, client, "shop"); !slices.Equal(got.GetTiers(), []environmentv1.Tier{productionTier}) {
		t.Errorf("GetProject(shop) tiers = %v, want production", got.GetTiers())
	}
}

type tierDeniedStore struct {
	records.Store
	denied environment.Tier
}

func (s tierDeniedStore) refuseDenied(tier environment.Tier) error {
	if tier == s.denied {
		return errors.New("AccessDenied: the credential reaches no " + string(tier) + " bootstrap")
	}
	return nil
}

func (s tierDeniedStore) Read(ctx context.Context, key records.Key) (records.Entry, error) {
	if err := s.refuseDenied(key.Partition.Tier); err != nil {
		return records.Entry{}, err
	}
	return s.Store.Read(ctx, key)
}

func (s tierDeniedStore) List(ctx context.Context, in records.Partition, under ...string) ([]records.Entry, error) {
	if err := s.refuseDenied(in.Tier); err != nil {
		return nil, err
	}
	return s.Store.List(ctx, in, under...)
}

type tierDeniedProvider struct {
	*fake.Provider
	store tierDeniedStore
}

func (p tierDeniedProvider) KeyValues() records.Store { return p.store }

func TestGetProjectReadsOnlyTheTiersItIsAskedFor(t *testing.T) {
	t.Parallel()
	vendor := fake.NewProvider(fake.Options{Region: "nowhere"})
	writeProject(t, vendor.KeyValues(), environment.TierProduction, "shop", relayProject)
	writeProject(t, vendor.KeyValues(), environment.TierPreview, "shop", relayProject)
	client := servedProvider(t, "1.0.0", tierDeniedProvider{Provider: vendor, store: tierDeniedStore{Store: vendor.KeyValues(), denied: environment.TierPreview}})

	if got := getProject(t, client, "shop", productionTier); !slices.Equal(got.GetTiers(), []environmentv1.Tier{productionTier}) {
		t.Errorf("GetProject(shop, production) tiers = %v, want production, read without touching preview", got.GetTiers())
	}
}

func TestGetProjectNamingNoTierIsRefused(t *testing.T) {
	t.Parallel()
	client, _ := contractServed(t, "1.0.0")

	_, err := client.GetProject(context.Background(), &contractv1.GetProjectRequest{Slug: "shop"})

	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("GetProject() naming no tier = %v, want it refused as invalid", err)
	}
}

func TestDescribeProjectOfAProjectNeverDeployedIsRefused(t *testing.T) {
	t.Parallel()
	client, _ := contractServed(t, "1.0.0")

	_, err := client.DescribeProject(context.Background(), &contractv1.DescribeProjectRequest{Slug: "shop"})

	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("DescribeProject() of a project never deployed = %v, want it refused as invalid", err)
	}
}

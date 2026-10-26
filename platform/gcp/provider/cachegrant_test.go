package gcp

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/credential"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/grants"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
	"google.golang.org/api/iam/v1"
)

func cachedAt(release naming.Coordinate) provider.StackSpec {
	spec := nextSpec()
	spec.App.ISR = &provider.ISRSpec{}
	spec.App.StorageCoordinate = release
	return spec
}

func cacheConditions(edgeBucket, cacheBucket, coordinate string) []string {
	var starts []string
	for _, kind := range []string{"fetch-cache", "use-cache", "tag-clock"} {
		starts = append(starts, `resource.name.startsWith("projects/_/buckets/`+cacheBucket+`/objects/`+kind+"/"+coordinate+`/")`)
	}
	return []string{
		strings.Join(starts, " || "),
		`resource.name.startsWith("projects/_/buckets/` + edgeBucket + `/objects/pages/` + coordinate + `/")`,
	}
}

func cacheBindings(member, edgeBucket, cacheBucket, coordinate string) []string {
	var bound []string
	for _, condition := range cacheConditions(edgeBucket, cacheBucket, coordinate) {
		bound = append(bound, grants.ObjectsRole+" "+member+" "+condition)
	}
	return bound
}

func cacheExpressionsOf(server *googletest.IAM, member string) []string {
	server.Mu.Lock()
	defer server.Mu.Unlock()
	var expressions []string
	for _, policy := range server.BucketPolicies {
		for _, binding := range policy.Bindings {
			if binding.Role == grants.ObjectsRole && (member == "" || slices.Contains(binding.Members, member)) {
				expressions = append(expressions, binding.Condition.Expression)
			}
		}
	}
	slices.Sort(expressions)
	return expressions
}

func cacheBindingsOf(server *googletest.IAM) []string {
	var bound []string
	for _, binding := range bucketBindingsOf(server) {
		if strings.HasPrefix(binding, grants.ObjectsRole+" ") {
			bound = append(bound, binding)
		}
	}
	return bound
}

func TestAnAppsAccountMayUseItsOwnEnvAndAppsCacheKindsAndNoOther(t *testing.T) {
	t.Parallel()
	server := appAccountsOnly()
	c := server.Open(t)
	member := "serviceAccount:" + c.AppAccountEmail("shop", stackrecords.ProductionEnv, "web")

	if err := applyAppGrants(t, c, cachedAt(cachedRelease())); err != nil {
		t.Fatalf("applyAppGrants() = %v", err)
	}

	want := cacheBindings(member, "ocel-prod-edge-k3x9q2", "ocel-prod-cache-k3x9q2", "shop/prod/web")
	if got := cacheBindingsOf(server); !slices.Equal(got, want) {
		t.Errorf("the buckets' storage bindings = %q, want exactly %q", got, want)
	}
	for _, policy := range server.BucketPolicies {
		for _, binding := range policy.Bindings {
			if binding.Role == grants.ObjectsRole && (binding.Condition == nil || binding.Condition.Title != "ocel cache of shop/prod/web") {
				t.Errorf("the cache binding's condition = %+v, want the title %q", binding.Condition, "ocel cache of shop/prod/web")
			}
		}
	}
	if got := projectBindingsOf(server); slices.ContainsFunc(got, func(bound string) bool { return strings.HasPrefix(bound, grants.ObjectsRole+" ") }) {
		t.Errorf("the project binds %q, and a cache grant on the project is one a deploy could hand itself over the other tier's buckets", got)
	}
}

func previewCachedAt(env string) provider.StackSpec {
	release := cachedRelease()
	release.Env = env
	spec := cachedAt(release)
	spec.Ref.Tier = environment.TierPreview
	spec.Ref.Name = naming.StackName{Env: env, App: "web"}
	return spec
}

func TestAPreviewEnvsAccountMayUseItsOwnCacheAndNoOtherPreviewEnvsOrProductions(t *testing.T) {
	t.Parallel()
	server := appAccountsOnly()
	c := server.Open(t)
	for _, env := range []string{"pr-7", "pr-8"} {
		if err := applyAppGrants(t, c, previewCachedAt(env)); err != nil {
			t.Fatalf("applyAppGrants(%s) = %v", env, err)
		}
	}

	var want []string
	for _, env := range []string{"pr-7", "pr-8"} {
		member := "serviceAccount:" + c.AppAccountEmail("shop", env, "web")
		want = append(want, cacheBindings(member, "ocel-preview-edge-k3x9q2", "ocel-preview-cache-k3x9q2", "shop/"+env+"/web")...)
	}
	slices.Sort(want)
	if got := cacheBindingsOf(server); !slices.Equal(got, want) {
		t.Errorf("the buckets' storage bindings = %q, want exactly %q: each preview's account reaches its own env's cache in the preview buckets and nothing beside", got, want)
	}
}

func TestTheCacheGrantPrefixIsTheOneTheRuntimeWritesUnder(t *testing.T) {
	t.Parallel()
	server := &googletest.Run{}
	p := providerServing(server, t)
	if _, err := p.ProvisionFunctions(context.Background(), routedNextSpec(), nil); err != nil {
		t.Fatalf("ProvisionFunctions() = %v", err)
	}
	env := googletest.EnvOf(server.Created[0].Template.Containers[0])

	expressions := cacheExpressionsOf(server.Identities(), "")
	coordinate := env["OCEL_STORAGE_COORDINATE"]
	if want := cacheConditions(env["OCEL_EDGE_BUCKET"], env["OCEL_CACHE_BUCKET"], coordinate[:strings.LastIndex(coordinate, "/")]); !slices.Equal(expressions, want) {
		t.Errorf("the grants are %q, want %q: every cache kind of the release at OCEL_STORAGE_COORDINATE %q", expressions, want, coordinate)
	}
}

func TestARedeployOfAnAppWritesNoPolicy(t *testing.T) {
	t.Parallel()
	server := appAccountsOnly()
	c := server.Open(t)
	if err := applyAppGrants(t, c, cachedAt(cachedRelease())); err != nil {
		t.Fatal(err)
	}
	before := server.ProjectWrites

	redeployed := cachedRelease()
	redeployed.Release = naming.NewReleaseToken("d2", "f2")
	if err := applyAppGrants(t, c, cachedAt(redeployed)); err != nil {
		t.Fatalf("second applyAppGrants() = %v", err)
	}
	if got := server.ProjectWrites - before; got != 0 {
		t.Errorf("a redeploy wrote the project policy %d times, want 0", got)
	}
}

func iamWrites(server *googletest.IAM) int {
	server.Mu.Lock()
	defer server.Mu.Unlock()
	return server.Writes + server.AccountWrites + server.TopicWrites + server.QueueWrites + server.BucketWrites
}

func TestARedeployOfANextAppWritesNoIAMPolicyAtAll(t *testing.T) {
	t.Parallel()
	server := appAccountsOnly()
	p, c := ensuringAccounts(t, server)
	first := routedNextSpec()
	if _, _, err := ensureAccount(p, context.Background(), c, first, nil); err != nil {
		t.Fatalf("EnsureAccount() = %v", err)
	}
	before := iamWrites(server)
	if before == 0 {
		t.Fatal("the first deploy set no IAM policy, so a redeploy setting none proves nothing")
	}

	redeployed := routedNextSpec()
	release := cachedRelease()
	release.Release = naming.NewReleaseToken("d2", "f2")
	redeployed.App.StorageCoordinate = release
	if _, _, err := ensureAccount(p, context.Background(), c, redeployed, nil); err != nil {
		t.Fatalf("second EnsureAccount() = %v", err)
	}
	if got := iamWrites(server) - before; got != 0 {
		t.Errorf("a redeploy of the same env and app set %d IAM policies, want none: its cache and asset grants are per env and app, not per release", got)
	}
}

func TestAnAppWithoutISRIsGrantedNoCache(t *testing.T) {
	t.Parallel()
	server := &googletest.Run{}
	p := providerServing(server, t)
	spec := functionStackDeclaring(environment.TierProduction, "production", provider.AppValues{})
	if _, err := p.ProvisionFunctions(context.Background(), spec, nil); err != nil {
		t.Fatalf("ProvisionFunctions() = %v", err)
	}
	if got := cacheBindingsOf(server.Identities()); len(got) != 0 {
		t.Errorf("an app without ISR holds the storage bindings %q, want none", got)
	}
}

func tagRecordBindingsOf(server *googletest.IAM) []string {
	var bound []string
	for _, binding := range projectBindingsOf(server) {
		if strings.HasPrefix(binding, credential.TaskRecordsRole+" ") && strings.Contains(binding, "/databases/"+googletest.Names.AppFence()) {
			bound = append(bound, binding)
		}
	}
	return bound
}

func TestANextAppsAccountMayUseTheTagRecordsOfTheDatabaseItsProjectKeepsThemInAndNoOther(t *testing.T) {
	t.Parallel()
	server := appAccountsOnly()
	c := server.Open(t)
	account := c.AppAccountEmail("shop", stackrecords.ProductionEnv, "web")
	shared := ports.SharedDatabase("ocel", environment.TierProduction)

	if err := applyAppGrants(t, c, routedNextSpec()); err != nil {
		t.Fatalf("applyAppGrants() = %v", err)
	}

	want := credential.TaskRecordsRole + " serviceAccount:" + account + ` resource.name == "projects/acme-prod/databases/` + shared + `"`
	if got := tagRecordBindingsOf(server); !slices.Equal(got, []string{want}) {
		t.Errorf("the project's tag record bindings = %q, want exactly %q", got, want)
	}
	for _, binding := range server.Project.Bindings {
		if binding.Role == credential.TaskRecordsRole && binding.Condition.Title != "ocel "+shared {
			t.Errorf("the tag records binding's condition = %+v, want the title %q", binding.Condition, "ocel "+shared)
		}
	}
}

func TestAnAppWithoutAnIncrementalCacheIsGrantedNoTagRecords(t *testing.T) {
	t.Parallel()
	server := appAccountsOnly()
	c := server.Open(t)

	if err := applyAppGrants(t, c, nextSpec()); err != nil {
		t.Fatalf("applyAppGrants() = %v", err)
	}
	if got := tagRecordBindingsOf(server); len(got) != 0 {
		t.Errorf("an app without ISR holds the tag record bindings %q, want none", got)
	}
}

func TestARedeployOfANextAppWritesNoTagRecordsPolicy(t *testing.T) {
	t.Parallel()
	server := appAccountsOnly()
	c := server.Open(t)
	if err := applyAppGrants(t, c, routedNextSpec()); err != nil {
		t.Fatal(err)
	}
	before := server.ProjectWrites

	if err := applyAppGrants(t, c, routedNextSpec()); err != nil {
		t.Fatalf("second applyAppGrants() = %v", err)
	}
	if got := server.ProjectWrites - before; got != 0 {
		t.Errorf("a redeploy wrote the project policy %d times, want 0", got)
	}
}

func TestANextContainersAccountMayUseItsOwnCachePrefixAndTagRecords(t *testing.T) {
	t.Parallel()
	server := &googletest.Run{}
	p := providerServing(server, t)
	if _, err := p.ProvisionContainers(context.Background(), nextContainerSpec(), nil); err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}
	env := googletest.EnvOf(server.Created[0].Template.Containers[0])
	account := "serviceAccount:" + server.Created[0].Template.ServiceAccount

	expressions := cacheExpressionsOf(server.Identities(), account)
	coordinate := env["OCEL_STORAGE_COORDINATE"]
	if want := cacheConditions(env["OCEL_EDGE_BUCKET"], env["OCEL_CACHE_BUCKET"], coordinate[:strings.LastIndex(coordinate, "/")]); !slices.Equal(expressions, want) {
		t.Errorf("the container's grants are %q, want %q: every cache kind of the release at OCEL_STORAGE_COORDINATE %q", expressions, want, coordinate)
	}
	if got := tagRecordBindingsOf(server.Identities()); len(got) != 1 || !strings.Contains(got[0], account) {
		t.Errorf("the project's tag record bindings = %q, want the one of %s", got, account)
	}
}

func writableDatabasesOf(server *googletest.IAM, member string) []string {
	server.Mu.Lock()
	defer server.Mu.Unlock()
	var databases []string
	for _, binding := range server.Project.Bindings {
		if binding.Role == "roles/datastore.user" && slices.Contains(binding.Members, member) && binding.Condition != nil {
			databases = append(databases, binding.Condition.Expression)
		}
	}
	slices.Sort(databases)
	return slices.Compact(databases)
}

func recordOwnDatabase(t *testing.T, c *gcloud.Clients, tier environment.Tier, project string) string {
	t.Helper()
	own := c.OwnDatabase(tier, project)
	value, err := ports.ProjectDatabaseRecord(own)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Records.Write(context.Background(), records.Entry{Key: ports.ProjectDatabaseKey(tier, project), Value: value}); err != nil {
		t.Fatal(err)
	}
	return own
}

func writtenDatabases(t *testing.T, server *googletest.IAM, p *Provider, c *gcloud.Clients, projects ...string) map[string][]string {
	t.Helper()
	written := map[string][]string{}
	for _, project := range projects {
		spec := reachingTopics(routedNextSpec())
		spec.Ref.Project = project
		email, _, err := ensureAccount(p, context.Background(), c, spec, map[string]*provider.TopicSpec{"resize": {}})
		if err != nil {
			t.Fatalf("ensureAppAccount(%s) = %v", project, err)
		}
		written[project] = writableDatabasesOf(server, "serviceAccount:"+email)
	}
	return written
}

func TestAnAppOfAProjectWithADatabaseOfItsOwnMayWriteThatDatabaseAloneAndNoOtherProjectsAppMay(t *testing.T) {
	t.Parallel()
	server := appAccountsOnly()
	server.AccountPolicies = map[string]*iam.Policy{}
	p, c := ensuringAccounts(t, server)
	own := recordOwnDatabase(t, c, environment.TierProduction, "shop")

	written := writtenDatabases(t, server, p, c, "shop", "blog")

	if want := []string{`resource.name == "projects/acme-prod/databases/` + own + `"`}; !slices.Equal(written["shop"], want) {
		t.Errorf("shop's app may write %q, want its own database alone %q", written["shop"], want)
	}
	for _, database := range written["blog"] {
		if strings.Contains(database, own) {
			t.Errorf("blog's app may write %s, the database shop keeps its runs, payloads and tag records in", database)
		}
	}
}

func TestTheAppsOfProjectsThatShareTheirTiersDatabaseMayWriteThatDatabaseAndNoOther(t *testing.T) {
	t.Parallel()
	server := appAccountsOnly()
	server.AccountPolicies = map[string]*iam.Policy{}
	p, c := ensuringAccounts(t, server)
	want := []string{`resource.name == "projects/acme-prod/databases/` + ports.SharedDatabase("ocel", environment.TierProduction) + `"`}

	for project, databases := range writtenDatabases(t, server, p, c, "shop", "blog") {
		if !slices.Equal(databases, want) {
			t.Errorf("%s's app may write %q, want the shared database alone %q", project, databases, want)
		}
	}
}

func TestARevalidatingNodeBuildIsGrantedNoPagesAndNothingOnAnEdgeBucketItsDeployNeverAdds(t *testing.T) {
	t.Parallel()
	tier := environment.TierProduction
	pre := provider.DeployPreflight{
		Deploy: provider.DeploySpec{Tier: tier, Apps: []provider.AppEntry{shippingApp("api", buildoutput.FrameworkNode)}},
		Uses:   []buildoutput.Uses{{Revalidation: true}},
	}
	parts, err := pushing(t, "").partsUsedBy(context.Background(), pre)
	if err != nil || slices.Contains(parts, bootstrap.EdgeBucketPart) {
		t.Fatalf("partsUsedBy() of a revalidating node build on the direct edge = %v, %v, want no edge bucket", parts, err)
	}

	c := &gcloud.Clients{Names: googletest.Names, Region: googletest.Region, Records: fake.NewKeyValues()}
	cache := googletest.TierBuckets(tier)[naming.StorageCache]
	if _, err := c.ClaimTierBucket(context.Background(), tier, naming.StorageCache, func() string { return cache }); err != nil {
		t.Fatal(err)
	}
	spec := cachedAt(cachedRelease())
	spec.App.Framework = buildoutput.FrameworkNode
	granted, err := cacheGrants(context.Background(), c, spec, "serviceAccount:api@acme-prod.iam.gserviceaccount.com")
	if err != nil {
		t.Fatalf("cacheGrants() = %v", err)
	}
	for _, grant := range granted {
		if grant.Target.Kind != grants.Bucket {
			continue
		}
		if grant.Target.Name != cache || strings.Contains(grant.Condition.Expression, "/objects/pages/") {
			t.Errorf("cacheGrants() of a node build grants %s on bucket %q (%s), want only its cache kinds in %s: only a Next app reads prerendered pages",
				grant.Role, grant.Target.Name, grant.Condition.Expression, cache)
		}
	}
}

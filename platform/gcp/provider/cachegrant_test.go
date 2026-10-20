package gcp

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/grants"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
	"google.golang.org/api/iam/v1"
)

func cachedAt(release naming.Coordinate) provider.StackSpec {
	spec := nextSpec()
	spec.App.ISR = &provider.ISRSpec{Coordinate: release}
	return spec
}

func cacheCondition(edgeBucket, cacheBucket, coordinate string) string {
	starts := []string{`resource.name.startsWith("projects/_/buckets/` + edgeBucket + `/objects/pages/` + coordinate + `/")`}
	for _, kind := range []string{"fetch-cache", "use-cache", "tag-clock"} {
		starts = append(starts, `resource.name.startsWith("projects/_/buckets/`+cacheBucket+`/objects/`+kind+"/"+coordinate+`/")`)
	}
	return strings.Join(starts, " || ")
}

func cacheBindingsOf(server *googletest.IAM) []string {
	var bound []string
	for _, binding := range projectBindingsOf(server) {
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
	member := "serviceAccount:" + c.AppAccountEmail(environment.TierProduction, "shop", stackrecords.ProductionEnv, "web")

	if err := applyAppGrants(t, c, cachedAt(cachedRelease())); err != nil {
		t.Fatalf("applyAppGrants() = %v", err)
	}

	want := grants.ObjectsRole + " " + member + " " + cacheCondition("ocel-prod-edge-k3x9q2", "ocel-prod-cache-k3x9q2", "shop/prod/web")
	if got := cacheBindingsOf(server); !slices.Equal(got, []string{want}) {
		t.Errorf("the project's storage bindings = %q, want exactly %q", got, want)
	}
	for _, binding := range server.Project.Bindings {
		if binding.Role == grants.ObjectsRole && (binding.Condition == nil || binding.Condition.Title != "ocel cache of shop/prod/web") {
			t.Errorf("the cache binding's condition = %+v, want the title %q", binding.Condition, "ocel cache of shop/prod/web")
		}
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
		member := "serviceAccount:" + c.AppAccountEmail(environment.TierPreview, "shop", env, "web")
		want = append(want, grants.ObjectsRole+" "+member+" "+cacheCondition("ocel-preview-edge-k3x9q2", "ocel-preview-cache-k3x9q2", "shop/"+env+"/web"))
	}
	slices.Sort(want)
	if got := cacheBindingsOf(server); !slices.Equal(got, want) {
		t.Errorf("the project's storage bindings = %q, want exactly %q: each preview's account reaches its own env's cache in the preview buckets and nothing beside", got, want)
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

	var expressions []string
	for _, binding := range server.Identities().Project.Bindings {
		if binding.Role == grants.ObjectsRole {
			expressions = append(expressions, binding.Condition.Expression)
		}
	}
	if len(expressions) != 1 {
		t.Fatalf("the project's storage bindings = %q, want the one binding of the app's account", expressions)
	}
	coordinate := env["OCEL_STORAGE_COORDINATE"]
	if want := cacheCondition(env["OCEL_EDGE_BUCKET"], env["OCEL_CACHE_BUCKET"], coordinate[:strings.LastIndex(coordinate, "/")]); expressions[0] != want {
		t.Errorf("the grant is %q, want %q: every cache kind of the release at OCEL_STORAGE_COORDINATE %q", expressions[0], want, coordinate)
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
	return server.Writes + server.AccountWrites + server.TopicWrites + server.QueueWrites
}

func TestARedeployOfANextAppWritesNoIAMPolicyAtAll(t *testing.T) {
	t.Parallel()
	server := appAccountsOnly()
	p, c := ensuringAccounts(t, server)
	first := routedNextSpec()
	if _, err := p.release().EnsureAccount(context.Background(), c, first, nil); err != nil {
		t.Fatalf("EnsureAccount() = %v", err)
	}
	before := iamWrites(server)
	if before == 0 {
		t.Fatal("the first deploy set no IAM policy, so a redeploy setting none proves nothing")
	}

	redeployed := routedNextSpec()
	release := cachedRelease()
	release.Release = naming.NewReleaseToken("d2", "f2")
	redeployed.App.ISR.Coordinate = release
	redeployed.App.AssetPrefix = release.AssetPrefix()
	if _, err := p.release().EnsureAccount(context.Background(), c, redeployed, nil); err != nil {
		t.Fatalf("second EnsureAccount() = %v", err)
	}
	if got := iamWrites(server) - before; got != 0 {
		t.Errorf("a redeploy of the same env and app set %d IAM policies, want none: its cache and asset grants are per env and app, not per release", got)
	}
}

func TestACachePolicyChangedUnderTheGrantIsReadAgainAndWritten(t *testing.T) {
	t.Parallel()
	server := &policyServer{refusals: 1}
	c := server.open(t)

	if err := applyAppGrants(t, c, cachedAt(cachedRelease())); err != nil {
		t.Fatalf("applyAppGrants() against a policy that changed once under it = %v", err)
	}
	if reads, writes := server.reads.Load(), server.writes.Load(); reads != 2 || writes != 2 {
		t.Errorf("the grant read %d and wrote %d times, want 2 and 2", reads, writes)
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
		if strings.HasPrefix(binding, tagRecordsRole+" ") && strings.Contains(binding, ports.ProjectWritableDatabasePrefix("ocel", environment.TierProduction)) {
			bound = append(bound, binding)
		}
	}
	return bound
}

func TestANextAppsAccountMayUseItsProjectsTagRecordsAndNoOtherDatabase(t *testing.T) {
	t.Parallel()
	server := appAccountsOnly()
	c := server.Open(t)
	account := c.AppAccountEmail(environment.TierProduction, "shop", stackrecords.ProductionEnv, "web")

	if err := applyAppGrants(t, c, routedNextSpec()); err != nil {
		t.Fatalf("applyAppGrants() = %v", err)
	}

	want := tagRecordsRole + " serviceAccount:" + account + ` resource.name == "projects/acme-prod/databases/` + ports.ProjectWritableDatabase("ocel", environment.TierProduction, "shop") + `"`
	if got := tagRecordBindingsOf(server); !slices.Equal(got, []string{want}) {
		t.Errorf("the project's tag record bindings = %q, want exactly %q", got, want)
	}
	for _, binding := range server.Project.Bindings {
		if binding.Role == tagRecordsRole && strings.Contains(binding.Condition.Expression, ports.ProjectWritableDatabasePrefix("ocel", environment.TierProduction)) &&
			binding.Condition.Title != "ocel shop production runs and tag records" {
			t.Errorf("the tag records binding's condition = %+v, want the title %q", binding.Condition, "ocel shop production runs and tag records")
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

	var expressions []string
	for _, binding := range server.Identities().Project.Bindings {
		if binding.Role == grants.ObjectsRole && slices.Contains(binding.Members, account) {
			expressions = append(expressions, binding.Condition.Expression)
		}
	}
	if len(expressions) != 1 {
		t.Fatalf("the container's account holds the storage bindings %q, want exactly one", expressions)
	}
	coordinate := env["OCEL_STORAGE_COORDINATE"]
	if want := cacheCondition(env["OCEL_EDGE_BUCKET"], env["OCEL_CACHE_BUCKET"], coordinate[:strings.LastIndex(coordinate, "/")]); expressions[0] != want {
		t.Errorf("the grant is %q, want %q: every cache kind of the release at OCEL_STORAGE_COORDINATE %q", expressions[0], want, coordinate)
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

func TestAnAppOfOneProjectMayWriteNoRunOrTagRecordOfAnotherProjectNorItsOwnValues(t *testing.T) {
	t.Parallel()
	server := appAccountsOnly()
	server.AccountPolicies = map[string]*iam.Policy{}
	p, c := ensuringAccounts(t, server)
	tier := environment.TierProduction

	written := map[string][]string{}
	for _, project := range []string{"shop", "blog"} {
		spec := reachingTopics(routedNextSpec())
		spec.Ref.Project = project
		email, err := p.release().EnsureAccount(context.Background(), c, spec, map[string]*provider.TopicSpec{"resize": {}})
		if err != nil {
			t.Fatalf("ensureAppAccount(%s) = %v", project, err)
		}
		written[project] = writableDatabasesOf(server, "serviceAccount:"+email)
	}

	if len(written["shop"]) == 0 {
		t.Fatalf("shop's app may write no database, want the one its runs and tag records are kept in")
	}
	for _, database := range written["shop"] {
		if slices.Contains(written["blog"], database) {
			t.Errorf("the apps of shop and blog may both write %s, so either reads and rewrites the other's runs, payloads and tag records", database)
		}
		if strings.Contains(database, `/databases/`+ports.ProjectDatabase("ocel", tier, "shop")+`"`) {
			t.Errorf("shop's app may write %s, the database its values are kept in", database)
		}
	}
}

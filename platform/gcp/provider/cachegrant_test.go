package gcp

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/grants"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
	"google.golang.org/api/iam/v1"
)

const cacheGrantPrefix = "prod/shop/web/r1a2b3c4d/isr"

func cachedAt(prefix string) provider.StackSpec {
	spec := nextSpec()
	spec.App.ISR = &provider.ISRSpec{Prefix: prefix}
	return spec
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

func TestAnAppsAccountMayUseObjectsUnderItsOwnCachePrefixAndNoOther(t *testing.T) {
	t.Parallel()
	server := appAccountsOnly()
	c := server.Open(t)
	member := "serviceAccount:" + c.AppAccountEmail(environment.TierProduction, "shop", stackrecords.ProductionEnv, "web")

	if err := applyAppGrants(t, c, cachedAt(cacheGrantPrefix)); err != nil {
		t.Fatalf("applyAppGrants() = %v", err)
	}

	want := grants.ObjectsRole + " " + member + ` resource.name.startsWith("projects/_/buckets/ocel-acme-prod-production/objects/cache/shop/web/")`
	if got := cacheBindingsOf(server); !slices.Equal(got, []string{want}) {
		t.Errorf("the project's storage bindings = %q, want exactly %q", got, want)
	}
	for _, binding := range server.Project.Bindings {
		if binding.Role == grants.ObjectsRole && (binding.Condition == nil || binding.Condition.Title != "ocel cache of shop/web") {
			t.Errorf("the cache binding's condition = %+v, want the title %q", binding.Condition, "ocel cache of shop/web")
		}
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
	opening := `resource.name.startsWith("projects/_/buckets/` + env["OCEL_ISR_BUCKET"] + `/objects/`
	if !strings.HasPrefix(expressions[0], opening) || !strings.HasSuffix(expressions[0], `")`) {
		t.Fatalf("the grant %q is not a startsWith over an object of bucket %q", expressions[0], env["OCEL_ISR_BUCKET"])
	}
	granted := strings.TrimSuffix(strings.TrimPrefix(expressions[0], opening), `")`)
	if !strings.HasPrefix(env["OCEL_ISR_OBJECT_PREFIX"]+"/", granted) {
		t.Errorf("the grant over %q is not a prefix of OCEL_ISR_OBJECT_PREFIX %q", granted, env["OCEL_ISR_OBJECT_PREFIX"])
	}
}

func TestARedeployOfAnAppWritesNoPolicy(t *testing.T) {
	t.Parallel()
	server := appAccountsOnly()
	c := server.Open(t)
	if err := applyAppGrants(t, c, cachedAt(cacheGrantPrefix)); err != nil {
		t.Fatal(err)
	}
	before := server.ProjectWrites

	if err := applyAppGrants(t, c, cachedAt(cacheGrantPrefix)); err != nil {
		t.Fatalf("second applyAppGrants() = %v", err)
	}
	if got := server.ProjectWrites - before; got != 0 {
		t.Errorf("a redeploy wrote the project policy %d times, want 0", got)
	}
}

func TestACachePolicyChangedUnderTheGrantIsReadAgainAndWritten(t *testing.T) {
	t.Parallel()
	server := &policyServer{refusals: 1}
	c := server.open(t)

	if err := applyAppGrants(t, c, cachedAt(cacheGrantPrefix)); err != nil {
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
	opening := `resource.name.startsWith("projects/_/buckets/` + env["OCEL_ISR_BUCKET"] + `/objects/`
	if !strings.HasPrefix(expressions[0], opening) || !strings.HasSuffix(expressions[0], `")`) {
		t.Fatalf("the grant %q is not a startsWith over an object of bucket %q", expressions[0], env["OCEL_ISR_BUCKET"])
	}
	granted := strings.TrimSuffix(strings.TrimPrefix(expressions[0], opening), `")`)
	if !strings.HasPrefix(env["OCEL_ISR_OBJECT_PREFIX"]+"/", granted) {
		t.Errorf("the grant over %q is not a prefix of OCEL_ISR_OBJECT_PREFIX %q", granted, env["OCEL_ISR_OBJECT_PREFIX"])
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

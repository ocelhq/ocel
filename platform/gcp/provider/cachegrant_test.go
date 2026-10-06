package gcp

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
)

const cacheGrantPrefix = "prod/shop/web/r1a2b3c4d/isr"

func cacheBindingsOf(server *iamServer) []string {
	var bound []string
	for _, binding := range projectBindingsOf(server) {
		if strings.HasPrefix(binding, appObjectsRole+" ") {
			bound = append(bound, binding)
		}
	}
	return bound
}

func TestAnAppsAccountMayUseObjectsUnderItsOwnCachePrefixAndNoOther(t *testing.T) {
	t.Parallel()
	server := appAccountsOnly()
	c := server.open(t)
	member := "serviceAccount:" + c.AppAccountEmail(environment.TierProduction, "shop", "web")

	if err := c.ensureCacheGrant(context.Background(), environment.TierProduction, member, cacheGrantPrefix); err != nil {
		t.Fatalf("ensureCacheGrant() = %v", err)
	}

	want := appObjectsRole + " " + member + ` resource.name.startsWith("projects/_/buckets/ocel-acme-prod-production/objects/cache/shop/web/")`
	if got := cacheBindingsOf(server); !slices.Equal(got, []string{want}) {
		t.Errorf("the project's storage bindings = %q, want exactly %q", got, want)
	}
	for _, binding := range server.project.Bindings {
		if binding.Role == appObjectsRole && (binding.Condition == nil || binding.Condition.Title != "ocel cache of shop/web") {
			t.Errorf("the cache binding's condition = %+v, want the title %q", binding.Condition, "ocel cache of shop/web")
		}
	}
}

func TestTheCacheGrantPrefixIsTheOneTheRuntimeWritesUnder(t *testing.T) {
	t.Parallel()
	server := &runServer{}
	p := server.open(t)
	if _, err := p.ProvisionFunctions(context.Background(), routedNextSpec(), nil); err != nil {
		t.Fatalf("ProvisionFunctions() = %v", err)
	}
	env := envOf(server.created[0].Template.Containers[0])

	var expressions []string
	for _, binding := range server.identities().project.Bindings {
		if binding.Role == appObjectsRole {
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
	c := server.open(t)
	member := "serviceAccount:" + c.AppAccountEmail(environment.TierProduction, "shop", "web")
	if err := c.ensureCacheGrant(context.Background(), environment.TierProduction, member, cacheGrantPrefix); err != nil {
		t.Fatal(err)
	}
	before := server.projectWrites

	if err := c.ensureCacheGrant(context.Background(), environment.TierProduction, member, cacheGrantPrefix); err != nil {
		t.Fatalf("second ensureCacheGrant() = %v", err)
	}
	if got := server.projectWrites - before; got != 0 {
		t.Errorf("a redeploy wrote the project policy %d times, want 0", got)
	}
}

func TestACachePolicyChangedUnderTheGrantIsReadAgainAndWritten(t *testing.T) {
	t.Parallel()
	server := &policyServer{refusals: 1}
	c := server.open(t)

	if err := c.ensureCacheGrant(context.Background(), environment.TierProduction, "serviceAccount:web@acme-prod.iam.gserviceaccount.com", cacheGrantPrefix); err != nil {
		t.Fatalf("ensureCacheGrant() against a policy that changed once under it = %v", err)
	}
	if reads, writes := server.reads.Load(), server.writes.Load(); reads != 2 || writes != 2 {
		t.Errorf("the grant read %d and wrote %d times, want 2 and 2", reads, writes)
	}
}

func TestAnAppWithoutISRIsGrantedNoCache(t *testing.T) {
	t.Parallel()
	server := &runServer{}
	p := server.open(t)
	spec := functionStackDeclaring(environment.TierProduction, "production", provider.AppValues{})
	if _, err := p.ProvisionFunctions(context.Background(), spec, nil); err != nil {
		t.Fatalf("ProvisionFunctions() = %v", err)
	}
	if got := cacheBindingsOf(server.identities()); len(got) != 0 {
		t.Errorf("an app without ISR holds the storage bindings %q, want none", got)
	}
}

func tagRecordBindingsOf(server *iamServer) []string {
	var bound []string
	for _, binding := range projectBindingsOf(server) {
		if strings.HasPrefix(binding, tagRecordsRole+" ") && strings.Contains(binding, "-tags") {
			bound = append(bound, binding)
		}
	}
	return bound
}

func TestANextAppsAccountMayUseItsTiersTagRecordsAndNoOtherDatabase(t *testing.T) {
	t.Parallel()
	server := appAccountsOnly()
	c := server.open(t)
	account := c.AppAccountEmail(environment.TierProduction, "shop", "web")

	if err := grantCache(context.Background(), c, routedNextSpec(), account); err != nil {
		t.Fatalf("grantCache() = %v", err)
	}

	want := tagRecordsRole + " serviceAccount:" + account + ` resource.name == "projects/acme-prod/databases/ocel-production-tags"`
	if got := tagRecordBindingsOf(server); !slices.Equal(got, []string{want}) {
		t.Errorf("the project's tag record bindings = %q, want exactly %q", got, want)
	}
	for _, binding := range server.project.Bindings {
		if binding.Role == tagRecordsRole && strings.Contains(binding.Condition.Expression, "-tags") &&
			binding.Condition.Title != "ocel ocel production tag records" {
			t.Errorf("the tag records binding's condition = %+v, want the title %q", binding.Condition, "ocel ocel production tag records")
		}
	}
}

func TestAnAppWithoutAnIncrementalCacheIsGrantedNoTagRecords(t *testing.T) {
	t.Parallel()
	server := appAccountsOnly()
	c := server.open(t)

	if err := grantCache(context.Background(), c, nextSpec(), c.AppAccountEmail(environment.TierProduction, "shop", "web")); err != nil {
		t.Fatalf("grantCache() = %v", err)
	}
	if got := tagRecordBindingsOf(server); len(got) != 0 {
		t.Errorf("an app without ISR holds the tag record bindings %q, want none", got)
	}
}

func TestARedeployOfANextAppWritesNoTagRecordsPolicy(t *testing.T) {
	t.Parallel()
	server := appAccountsOnly()
	c := server.open(t)
	account := c.AppAccountEmail(environment.TierProduction, "shop", "web")
	if err := grantCache(context.Background(), c, routedNextSpec(), account); err != nil {
		t.Fatal(err)
	}
	before := server.projectWrites

	if err := grantCache(context.Background(), c, routedNextSpec(), account); err != nil {
		t.Fatalf("second grantCache() = %v", err)
	}
	if got := server.projectWrites - before; got != 0 {
		t.Errorf("a redeploy wrote the project policy %d times, want 0", got)
	}
}

package gcp

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"

	"google.golang.org/api/iam/v1"
	raw "google.golang.org/api/storage/v1"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/grants"
)

func servingAccountsAndBuckets(t *testing.T) (*Provider, *gcloud.Clients, *googletest.IAM, *storageServer) {
	t.Helper()
	accounts := appAccountsOnly()
	accounts.AccountPolicies = map[string]*iam.Policy{}
	buckets := &storageServer{buckets: map[string]*raw.Bucket{}, policies: map[string]*raw.Policy{}, objects: map[string][]string{}}
	rest := accounts.Rest(t)
	c := accounts.Serve(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/storage/v1/") {
			buckets.serve()(w, r)
			return
		}
		rest(w, r)
	})
	return pushing(t, c.Endpoint), c, accounts, buckets
}

func bindingBucket(t *testing.T, c *gcloud.Clients, name string) provider.Binding {
	t.Helper()
	return provider.Binding{
		Type:       provider.BindingBucket,
		Name:       name,
		Properties: map[string]string{provider.PropertyBucket: appBucketOf(t, c.Names, "prod", name)},
	}
}

func existingBuckets(buckets *storageServer, names ...string) {
	for _, name := range names {
		buckets.holding(&raw.Bucket{Name: name})
	}
}

func recordingBucketStacks(t *testing.T, spec provider.StackSpec, bound ...provider.Binding) *fake.KeyValues {
	t.Helper()
	records := fake.NewKeyValues()
	if err := stackrecords.Write(context.Background(), records, spec.Ref.Tier, spec.Ref.Project, spec.Ref.Name, stackrecords.Stack{Kind: provider.StackApp, App: "web"}); err != nil {
		t.Fatal(err)
	}
	if err := stackrecords.Write(context.Background(), records, spec.Ref.Tier, spec.Ref.Project, naming.InfraStack(stackrecords.ProductionEnv),
		stackrecords.Stack{Kind: provider.StackInfra, Bindings: bound}); err != nil {
		t.Fatal(err)
	}
	return records
}

func TestAnAppIsGrantedTheObjectsOfTheBucketsItBindsAndNoOtherBucket(t *testing.T) {
	t.Parallel()
	p, c, _, buckets := servingAccountsAndBuckets(t)
	uploads, avatars := appBucketOf(t, c.Names, "prod", "uploads"), appBucketOf(t, c.Names, "prod", "avatars")
	existingBuckets(buckets, uploads, avatars)
	spec := functionStackDeclaring(environment.TierProduction, "production", provider.AppValues{})
	web, api := spec, spec
	web.App, api.App = new(provider.AppSpec), new(provider.AppSpec)
	*web.App, *api.App = *spec.App, *spec.App
	web = appNamed("web", web)
	web.App.Values.Bindings = []provider.Binding{bindingBucket(t, c, "uploads")}
	api = appNamed("api", api)
	api.App.Grants = []provider.Binding{bindingBucket(t, c, "avatars")}

	webAccount, err := p.release().EnsureAccount(context.Background(), c, web, nil)
	if err != nil {
		t.Fatalf("ensureAppAccount(web) = %v", err)
	}
	apiAccount, err := p.release().EnsureAccount(context.Background(), c, api, nil)
	if err != nil {
		t.Fatalf("ensureAppAccount(api) = %v", err)
	}

	for _, tc := range []struct {
		bucket, member string
		want           []string
	}{
		{uploads, "serviceAccount:" + webAccount, []string{"roles/storage.objectAdmin"}},
		{uploads, "serviceAccount:" + apiAccount, nil},
		{avatars, "serviceAccount:" + apiAccount, []string{"roles/storage.objectAdmin"}},
		{avatars, "serviceAccount:" + webAccount, nil},
	} {
		if got := rolesOf(buckets.granted(tc.bucket), tc.member); !slices.Equal(got, tc.want) {
			t.Errorf("%s holds %v on %s, want %v: an app reaches the buckets it binds and no other app's", tc.member, got, tc.bucket, tc.want)
		}
	}
}

func TestAnAppBindingNoBucketHoldsNoBucketGrantAndCannotSignAsItself(t *testing.T) {
	t.Parallel()
	p, c, accounts, buckets := servingAccountsAndBuckets(t)
	uploads := appBucketOf(t, c.Names, "prod", "uploads")
	existingBuckets(buckets, uploads)

	account, err := p.release().EnsureAccount(context.Background(), c, appNamed("web", functionStackDeclaring(environment.TierProduction, "production", provider.AppValues{})), nil)
	if err != nil {
		t.Fatalf("ensureAppAccount() = %v", err)
	}

	if got := rolesOf(buckets.granted(uploads), "serviceAccount:"+account); got != nil {
		t.Errorf("an app binding no bucket holds %v on one", got)
	}
	for _, held := range holdings(accounts, c, "serviceAccount:"+account) {
		if strings.Contains(held, grants.TokenCreatorRole) {
			t.Errorf("an app binding no bucket holds %q, and it signs no bucket url", held)
		}
	}
}

func TestAnAppBindingABucketMaySignAsItsOwnAccount(t *testing.T) {
	t.Parallel()
	p, c, accounts, buckets := servingAccountsAndBuckets(t)
	existingBuckets(buckets, appBucketOf(t, c.Names, "prod", "uploads"))
	spec := appNamed("web", functionStackDeclaring(environment.TierProduction, "production", provider.AppValues{}))
	spec.App.Values.Bindings = []provider.Binding{bindingBucket(t, c, "uploads")}

	account, err := p.release().EnsureAccount(context.Background(), c, spec, nil)
	if err != nil {
		t.Fatalf("ensureAppAccount() = %v", err)
	}

	want := "own account " + grants.TokenCreatorRole
	if got := holdings(accounts, c, "serviceAccount:"+account); !slices.Contains(got, want) {
		t.Errorf("the account holds %q, want %q: a signed url is signed through IAM as the account itself", got, want)
	}
}

func TestAnAppsBucketGrantRacingAnotherWriteIsRetriedOnTheFreshPolicy(t *testing.T) {
	t.Parallel()
	p, c, _, buckets := servingAccountsAndBuckets(t)
	uploads := appBucketOf(t, c.Names, "prod", "uploads")
	existingBuckets(buckets, uploads)
	buckets.staleSets = 1
	spec := appNamed("web", functionStackDeclaring(environment.TierProduction, "production", provider.AppValues{}))
	spec.App.Values.Bindings = []provider.Binding{bindingBucket(t, c, "uploads")}

	account, err := p.release().EnsureAccount(context.Background(), c, spec, nil)
	if err != nil {
		t.Fatalf("ensureAppAccount() with a policy written under it = %v, want the grant retried", err)
	}

	if got := rolesOf(buckets.granted(uploads), "serviceAccount:"+account); len(got) != 1 {
		t.Errorf("the app holds %v after the retry, want the grant to have landed", got)
	}
}

func TestAnAppBindingABucketThatIsGoneIsRefusedNamingTheBucket(t *testing.T) {
	t.Parallel()
	p, c, _, _ := servingAccountsAndBuckets(t)
	spec := appNamed("web", functionStackDeclaring(environment.TierProduction, "production", provider.AppValues{}))
	spec.App.Values.Bindings = []provider.Binding{bindingBucket(t, c, "uploads")}

	_, err := p.release().EnsureAccount(context.Background(), c, spec, nil)

	if err == nil || !strings.Contains(err.Error(), appBucketOf(t, c.Names, "prod", "uploads")) {
		t.Errorf("ensureAppAccount() = %v, want an error naming the bucket it could not grant", err)
	}
}

func TestADeployThatStopsBindingABucketTakesTheAppsGrantOnItAway(t *testing.T) {
	t.Parallel()
	p, c, _, buckets := servingAccountsAndBuckets(t)
	uploads, avatars := appBucketOf(t, c.Names, "prod", "uploads"), appBucketOf(t, c.Names, "prod", "avatars")
	existingBuckets(buckets, uploads, avatars)
	spec := appNamed("web", functionStackDeclaring(environment.TierProduction, "production", provider.AppValues{}))
	spec.Ref.Name = stackOf(stackrecords.ProductionEnv, "web", "r1")
	p.records = recordingBucketStacks(t, spec, bindingBucket(t, c, "uploads"), bindingBucket(t, c, "avatars"))
	spec.App.Values.Bindings = []provider.Binding{bindingBucket(t, c, "uploads"), bindingBucket(t, c, "avatars")}
	account, err := p.release().EnsureAccount(context.Background(), c, spec, nil)
	if err != nil {
		t.Fatalf("ensureAppAccount() = %v", err)
	}
	member := "serviceAccount:" + account
	spec.App.Values.Bindings = []provider.Binding{bindingBucket(t, c, "uploads")}

	if _, err := p.release().EnsureAccount(context.Background(), c, spec, nil); err != nil {
		t.Fatalf("ensureAppAccount() without the avatars binding = %v", err)
	}

	if got := rolesOf(buckets.granted(avatars), member); got != nil {
		t.Errorf("the account holds %v on %s after a deploy that no longer binds it", got, avatars)
	}
	if got := rolesOf(buckets.granted(uploads), member); len(got) != 1 {
		t.Errorf("the account holds %v on %s, want the grant of the bucket it still binds", got, uploads)
	}
}

func TestADeployThatStopsBindingABucketAlreadyDeletedIsNotRefused(t *testing.T) {
	t.Parallel()
	p, c, _, _ := servingAccountsAndBuckets(t)
	spec := appNamed("web", functionStackDeclaring(environment.TierProduction, "production", provider.AppValues{}))
	spec.Ref.Name = stackOf(stackrecords.ProductionEnv, "web", "r1")
	p.records = recordingBucketStacks(t, spec, bindingBucket(t, c, "uploads"))

	if _, err := p.release().EnsureAccount(context.Background(), c, spec, nil); err != nil {
		t.Errorf("ensureAppAccount() with a recorded bucket already deleted = %v, want nothing to revoke", err)
	}
}

func TestRemovingTheLastReleaseOfAnEnvironmentRevokesItsBuckets(t *testing.T) {
	t.Parallel()
	p, c, _, buckets := servingAccountsAndBuckets(t)
	uploads := appBucketOf(t, c.Names, "prod", "uploads")
	existingBuckets(buckets, uploads)
	spec := appNamed("web", functionStackDeclaring(environment.TierProduction, "production", provider.AppValues{}))
	spec.Ref.Name = stackOf(stackrecords.ProductionEnv, "web", "r1")
	spec.App.Values.Bindings = []provider.Binding{bindingBucket(t, c, "uploads")}
	account, err := p.release().EnsureAccount(context.Background(), c, spec, nil)
	if err != nil {
		t.Fatalf("ensureAppAccount() = %v", err)
	}

	if err := p.release().RevokeAfterRemoval(context.Background(), c, spec.Ref, nil, nil, nil); err != nil {
		t.Fatalf("revokeAfterRemoval() = %v", err)
	}

	if got := rolesOf(buckets.granted(uploads), "serviceAccount:"+account); got != nil {
		t.Errorf("the account still holds %v on %s after its environment's last release was removed", got, uploads)
	}
}

func TestRevokingAnAppsBucketsToleratesABucketAlreadyDeleted(t *testing.T) {
	t.Parallel()
	p, c, _, buckets := servingAccountsAndBuckets(t)
	uploads := appBucketOf(t, c.Names, "prod", "uploads")
	existingBuckets(buckets, uploads)
	spec := appNamed("web", functionStackDeclaring(environment.TierProduction, "production", provider.AppValues{}))
	spec.Ref.Name = stackOf(stackrecords.ProductionEnv, "web", "r1")
	spec.App.Values.Bindings = []provider.Binding{bindingBucket(t, c, "uploads")}
	if _, err := p.release().EnsureAccount(context.Background(), c, spec, nil); err != nil {
		t.Fatalf("ensureAppAccount() = %v", err)
	}
	buckets.mu.Lock()
	delete(buckets.buckets, uploads)
	delete(buckets.policies, uploads)
	buckets.mu.Unlock()

	if err := p.release().RevokeAfterRemoval(context.Background(), c, spec.Ref, nil, nil, nil); err != nil {
		t.Errorf("revokeAfterRemoval() with its bucket already deleted = %v, want nothing to revoke", err)
	}
}

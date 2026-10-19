package gcp

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"

	raw "google.golang.org/api/storage/v1"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
	s3store "github.com/ocelhq/ocel/platform/s3"
)

var cloudStorageName = regexp.MustCompile(`^[a-z0-9][-a-z0-9]{1,61}[a-z0-9]$`)

func TestAnAppBucketIsNamedForTheNamespaceProjectEnvironmentAndDeclaredBucket(t *testing.T) {
	named := names.New("ocel", "acme-prod")

	bucket := named.AppBucket("shop", "prod", "uploads")
	if !strings.HasPrefix(bucket, "ocel--shop-prod-uploads-") {
		t.Errorf("AppBucket() = %q, want the namespace, project, environment and bucket readable at its start", bucket)
	}
	if !cloudStorageName.MatchString(bucket) {
		t.Errorf("AppBucket() = %q, which Cloud Storage will not take as a bucket name", bucket)
	}
	if other := (names.New("ocel", "acme-staging")).AppBucket("shop", "prod", "uploads"); other == bucket {
		t.Errorf("two Google Cloud projects name the same bucket %q, and a bucket name is global: the second deploy would find the first one's bucket taken", bucket)
	}
	if preview := named.AppBucket("shop", "pr-7", "uploads"); preview == bucket {
		t.Errorf("both environments store into %q, and a preview would write over production's objects", bucket)
	}
}

func TestNoAppBucketOfAnotherNamespaceStartsWithThisNamespacesBucketPrefix(t *testing.T) {
	prefix := names.New("ocel", "acme-prod").AppBucketPrefix()
	for _, namespace := range []provider.Namespace{"ocel-dev", "ocel-shop", "ocelot"} {
		other := names.New(namespace, "acme-prod").AppBucket("shop", "prod", "uploads")
		if strings.HasPrefix(other, prefix) {
			t.Errorf("namespace %s names its bucket %q, under namespace ocel's prefix %q, so ocel's deploy credential administers it", namespace, other, prefix)
		}
	}
	if own := (names.New("ocel", "acme-prod")).AppBucket("dev", "prod", "uploads"); !strings.HasPrefix(own, prefix) {
		t.Errorf("AppBucket() = %q, want it under the namespace's prefix %q", own, prefix)
	}
}

func TestAnAppBucketWhoseReadableNameRunsLongIsCutToWhatCloudStorageTakes(t *testing.T) {
	names := names.New("ocel", "acme-prod")

	long := names.AppBucket(strings.Repeat("storefront", 4), "feature-branch-preview", strings.Repeat("user-uploads", 3))
	if !cloudStorageName.MatchString(long) {
		t.Errorf("AppBucket() = %q (%d characters), which Cloud Storage will not take as a bucket name", long, len(long))
	}
	cut := names.AppBucket(strings.Repeat("storefront", 4), "feature-branch-preview", strings.Repeat("user-uploads", 3)+"-2")
	if cut == long {
		t.Errorf("two buckets whose names differ past the cut share %q", long)
	}
}

func aBucket(spec provider.BucketSpec) resources.ProvisionRequest {
	return resources.ProvisionRequest{
		Ref:      provider.StackRef{Project: "shop", Tier: environment.TierProduction, Name: naming.InfraStack("prod")},
		Resource: provider.Resource{Name: "uploads", Declared: "uploads", Type: provider.BindingBucket, Bucket: &spec},
	}
}

func rolesOf(policy *raw.Policy, member string) []string {
	var roles []string
	if policy == nil {
		return nil
	}
	for _, binding := range policy.Bindings {
		if slices.Contains(binding.Members, member) {
			roles = append(roles, binding.Role)
		}
	}
	return roles
}

func TestABucketIsCreatedPrivateInTheRegionAndGrantedToNoAccount(t *testing.T) {
	p, server := servingStorage(t)

	binding, err := p.bucketHooks().Provision(context.Background(), aBucket(provider.BucketSpec{}), nil)
	if err != nil {
		t.Fatalf("ProvisionBucket() = %v", err)
	}

	name := namesOf(t, p).AppBucket("shop", "prod", "uploads")
	if len(server.created) != 1 {
		t.Fatalf("Cloud Storage was asked to create %d buckets, want one per declared bucket", len(server.created))
	}
	created := server.created[0]
	if created.Name != name || !strings.EqualFold(created.Location, "europe-west1") {
		t.Errorf("created %q in %q, want %q in the deploy's region", created.Name, created.Location, name)
	}
	if created.IamConfiguration == nil || created.IamConfiguration.UniformBucketLevelAccess == nil || !created.IamConfiguration.UniformBucketLevelAccess.Enabled {
		t.Error("the bucket is created with object ACLs live, and an ACL on one object would reach past the IAM the deploy grants")
	}
	if created.IamConfiguration == nil || created.IamConfiguration.PublicAccessPrevention != "enforced" {
		t.Errorf("the bucket is created under public access prevention %+v, want it enforced: an allUsers grant would publish every object in it", created.IamConfiguration)
	}
	if created.Labels["ocel-bucket"] != "uploads" || created.Labels["ocel-environment"] != "prod" || created.Labels["ocel-namespace"] != "ocel" {
		t.Errorf("created labels %v, want the namespace, environment and declared bucket it stores", created.Labels)
	}
	if created.Lifecycle == nil || !slices.ContainsFunc(created.Lifecycle.Rule, func(rule *raw.BucketLifecycleRule) bool {
		return rule.Action.Type == "AbortIncompleteMultipartUpload" && rule.Condition.Age != nil && *rule.Condition.Age == 1
	}) {
		t.Fatalf("created lifecycle %s, want multipart uploads abandoned for a day aborted, or their parts bill forever", googletest.JSON(t, created.Lifecycle))
	}
	if !slices.ContainsFunc(created.Lifecycle.Rule, func(rule *raw.BucketLifecycleRule) bool {
		return rule.Action.Type == "Delete" && slices.Equal(rule.Condition.MatchesPrefix, []string{s3store.SessionKeyPrefix})
	}) {
		t.Errorf("created lifecycle %s, want the upload sessions the runtime keeps deleted once they have long expired", googletest.JSON(t, created.Lifecycle))
	}
	if policy := server.granted(name); policy != nil && len(policy.Bindings) != 0 {
		t.Errorf("a freshly provisioned bucket is bound to %s, want no account: an app is granted the buckets it binds when it deploys", googletest.JSON(t, policy.Bindings))
	}
	if binding.Type != provider.BindingBucket || binding.Name != "uploads" || binding.Properties[provider.PropertyBucket] != name {
		t.Errorf("ProvisionBucket() = %+v, want a bucket binding for uploads naming %s", binding, name)
	}
}

func TestABucketAllowsBrowserUploadsFromTheOriginsItDeclares(t *testing.T) {
	p, server := servingStorage(t)

	if _, err := p.bucketHooks().Provision(context.Background(), aBucket(provider.BucketSpec{AllowedOrigins: []string{"https://shop.example"}}), nil); err != nil {
		t.Fatalf("ProvisionBucket() = %v", err)
	}

	cors := server.created[0].Cors
	if len(cors) != 1 || !slices.Equal(cors[0].Origin, []string{"https://shop.example"}) ||
		!slices.Contains(cors[0].Method, http.MethodPut) || !slices.Contains(cors[0].Method, http.MethodPost) ||
		!slices.Contains(cors[0].ResponseHeader, "ETag") {
		t.Errorf("created CORS %s, want PUT and POST from https://shop.example with ETag readable, as a multipart upload needs", googletest.JSON(t, cors))
	}
}

func TestABucketAlreadyInPlaceIsMendedOnlyWhereItDrifted(t *testing.T) {
	p, server := servingStorage(t)
	if _, err := p.bucketHooks().Provision(context.Background(), aBucket(provider.BucketSpec{}), nil); err != nil {
		t.Fatal(err)
	}

	if _, err := p.bucketHooks().Provision(context.Background(), aBucket(provider.BucketSpec{}), nil); err != nil {
		t.Fatalf("ProvisionBucket() = %v", err)
	}
	if len(server.created) != 1 || len(server.patched) != 0 || len(server.setters) != 0 {
		t.Errorf("a second deploy created %d, patched %d and set %d policies, want nothing written over a bucket already as declared",
			len(server.created)-1, len(server.patched), len(server.setters))
	}

	if _, err := p.bucketHooks().Provision(context.Background(), aBucket(provider.BucketSpec{AllowedOrigins: []string{"https://shop.example"}}), nil); err != nil {
		t.Fatalf("ProvisionBucket() = %v", err)
	}
	if len(server.patched) != 1 || len(server.patched[0].Cors) != 1 {
		t.Errorf("declaring an origin patched %s, want the bucket's CORS patched to it", googletest.JSON(t, server.patched))
	}
}

func TestABucketNameAnotherAccountHoldsIsRefusedWithWhatToRename(t *testing.T) {
	p, server := servingStorage(t)
	server.takenElsewhere = true

	_, err := p.bucketHooks().Provision(context.Background(), aBucket(provider.BucketSpec{}), nil)
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid || !strings.Contains(err.Error(), "uploads") {
		t.Fatalf("ProvisionBucket() over a name taken elsewhere = %v, want an %s refusal naming the bucket to rename", err, refusal.CodeInvalid)
	}
}

func TestRemovingABucketDeletesItsObjectsThenTheBucket(t *testing.T) {
	p, server := servingStorage(t)
	name := namesOf(t, p).AppBucket("shop", "prod", "uploads")
	server.holding(&raw.Bucket{Name: name}, "avatars/one.png", s3store.SessionKeyPrefix+"sess_1")

	binding := provider.Binding{Type: provider.BindingBucket, Name: "uploads", Properties: map[string]string{provider.PropertyBucket: name}}
	if err := p.bucketHooks().Remove(context.Background(), aBucket(provider.BucketSpec{}).Ref, binding, nil); err != nil {
		t.Fatalf("RemoveBucket() = %v", err)
	}
	if len(server.removed) != 2 || !slices.Equal(server.deleted, []string{name}) {
		t.Errorf("removal deleted objects %v and buckets %v, want every object and then %s", server.removed, server.deleted, name)
	}

	if err := p.bucketHooks().Remove(context.Background(), aBucket(provider.BucketSpec{}).Ref, binding, nil); err != nil {
		t.Errorf("RemoveBucket() of a bucket already gone = %v, want nothing to do", err)
	}
}

func TestABootstrapChecksTheAPIAppsSignTheirBucketURLsThroughIsOn(t *testing.T) {
	t.Parallel()

	if !slices.Contains(bootstrap.APIsFor(environment.TierProduction, nil), "iamcredentials.googleapis.com") {
		t.Errorf("a bootstrap checks %v are on, want iamcredentials.googleapis.com among them: an app signs its buckets' URLs through it, and every signed URL fails while it is off", bootstrap.APIsFor(environment.TierProduction, nil))
	}
}

func planBucket(t *testing.T, p *Provider, server *storageServer, spec provider.BucketSpec) (provider.ChangeAction, string) {
	t.Helper()
	before := len(server.wrote())
	action, reason, err := p.bucketHooks().Plan(context.Background(), aBucket(spec), nil)
	if err != nil {
		t.Fatalf("PlanBucket() = %v", err)
	}
	if wrote := server.wrote()[before:]; len(wrote) != 0 {
		t.Errorf("PlanBucket() wrote %q to Cloud Storage, want a plan that only reads", wrote)
	}
	return action, reason
}

func TestABucketWhoseAllowedOriginsChangePlansAnUpdate(t *testing.T) {
	p, server := servingStorage(t)
	if _, err := p.bucketHooks().Provision(context.Background(), aBucket(provider.BucketSpec{AllowedOrigins: []string{"https://a.example"}}), nil); err != nil {
		t.Fatal(err)
	}

	action, reason := planBucket(t, p, server, provider.BucketSpec{AllowedOrigins: []string{"https://a.example", "https://b.example"}})
	if action != provider.ActionUpdate {
		t.Fatalf("PlanBucket() = %s, want %s: the provision that follows widens the bucket's CORS", action, provider.ActionUpdate)
	}
	if want := "allowed origins change from [https://a.example] to [https://a.example https://b.example]"; !strings.Contains(reason, want) {
		t.Errorf("PlanBucket() reason = %q, want it to say %q", reason, want)
	}
}

func TestABucketWhoseLifecycleRulesDriftedPlansAnUpdate(t *testing.T) {
	p, server := servingStorage(t)
	if _, err := p.bucketHooks().Provision(context.Background(), aBucket(provider.BucketSpec{}), nil); err != nil {
		t.Fatal(err)
	}
	server.drift(namesOf(t, p).AppBucket("shop", "prod", "uploads"), func(bucket *raw.Bucket) { bucket.Lifecycle = nil })

	action, reason := planBucket(t, p, server, provider.BucketSpec{})
	if action != provider.ActionUpdate || !strings.Contains(reason, "lifecycle rules change") {
		t.Errorf("PlanBucket() = %s %q, want an update naming the lifecycle rules the provision rewrites", action, reason)
	}
}

func TestABucketWhosePublicAccessPreventionWasLiftedPlansAnUpdate(t *testing.T) {
	p, server := servingStorage(t)
	if _, err := p.bucketHooks().Provision(context.Background(), aBucket(provider.BucketSpec{}), nil); err != nil {
		t.Fatal(err)
	}
	server.drift(namesOf(t, p).AppBucket("shop", "prod", "uploads"), func(bucket *raw.Bucket) {
		bucket.IamConfiguration = &raw.BucketIamConfiguration{
			PublicAccessPrevention:   "inherited",
			UniformBucketLevelAccess: &raw.BucketIamConfigurationUniformBucketLevelAccess{},
		}
	})

	action, reason := planBucket(t, p, server, provider.BucketSpec{})
	if action != provider.ActionUpdate || !strings.Contains(reason, "public access prevention changes from inherited to enforced") ||
		!strings.Contains(reason, "uniform bucket-level access turns on") {
		t.Errorf("PlanBucket() = %s %q, want an update naming the access the provision restores", action, reason)
	}
}

func TestABucketOnAnEmulatorThatKeepsNoAccessConfigurationPlansKeepAndIsNotRewritten(t *testing.T) {
	p, server := servingStorage(t)
	if _, err := p.bucketHooks().Provision(context.Background(), aBucket(provider.BucketSpec{}), nil); err != nil {
		t.Fatal(err)
	}
	server.drift(namesOf(t, p).AppBucket("shop", "prod", "uploads"), func(bucket *raw.Bucket) { bucket.IamConfiguration = nil })

	if action, reason := planBucket(t, p, server, provider.BucketSpec{}); action != provider.ActionKeep {
		t.Errorf("PlanBucket() = %s %q, want %s: the emulator keeps no access configuration, so a rerun would plan, and make, an update forever", action, reason, provider.ActionKeep)
	}
	before := len(server.wrote())
	if _, err := p.bucketHooks().Provision(context.Background(), aBucket(provider.BucketSpec{}), nil); err != nil {
		t.Fatal(err)
	}
	if wrote := server.wrote()[before:]; len(wrote) != 0 {
		t.Errorf("a rerun of the provision wrote %q, want nothing: the bucket is as declared but for what the emulator cannot keep", wrote)
	}
}

func TestABucketAlreadyAsDeclaredPlansKeep(t *testing.T) {
	p, server := servingStorage(t)
	spec := provider.BucketSpec{AllowedOrigins: []string{"https://shop.example"}}
	if _, err := p.bucketHooks().Provision(context.Background(), aBucket(spec), nil); err != nil {
		t.Fatal(err)
	}

	if action, reason := planBucket(t, p, server, spec); action != provider.ActionKeep || reason != provider.ReasonCurrent {
		t.Errorf("PlanBucket() = %s %q, want %s %q", action, reason, provider.ActionKeep, provider.ReasonCurrent)
	}
}

func TestAMissingBucketPlansACreate(t *testing.T) {
	p, server := servingStorage(t)

	if action, _ := planBucket(t, p, server, provider.BucketSpec{}); action != provider.ActionCreate {
		t.Errorf("PlanBucket() = %s, want %s", action, provider.ActionCreate)
	}
	if len(server.created) != 0 {
		t.Errorf("PlanBucket() created %d buckets, want none", len(server.created))
	}
}

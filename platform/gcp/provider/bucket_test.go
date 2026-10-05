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
	s3store "github.com/ocelhq/ocel/platform/s3"
)

var cloudStorageName = regexp.MustCompile(`^[a-z0-9][-a-z0-9]{1,61}[a-z0-9]$`)

func TestAnAppBucketIsNamedForTheNamespaceProjectEnvironmentAndDeclaredBucket(t *testing.T) {
	names := Names{namespace: "ocel", project: "acme-prod"}

	bucket := names.AppBucket("shop", "prod", "uploads")
	if !strings.HasPrefix(bucket, "ocel--shop-prod-uploads-") {
		t.Errorf("AppBucket() = %q, want the namespace, project, environment and bucket readable at its start", bucket)
	}
	if !cloudStorageName.MatchString(bucket) {
		t.Errorf("AppBucket() = %q, which Cloud Storage will not take as a bucket name", bucket)
	}
	if other := (Names{namespace: "ocel", project: "acme-staging"}).AppBucket("shop", "prod", "uploads"); other == bucket {
		t.Errorf("two Google Cloud projects name the same bucket %q, and a bucket name is global: the second deploy would find the first one's bucket taken", bucket)
	}
	if preview := names.AppBucket("shop", "pr-7", "uploads"); preview == bucket {
		t.Errorf("both environments store into %q, and a preview would write over production's objects", bucket)
	}
}

func TestNoAppBucketOfAnotherNamespaceStartsWithThisNamespacesBucketPrefix(t *testing.T) {
	prefix := Names{namespace: "ocel", project: "acme-prod"}.AppBucketPrefix()
	for _, namespace := range []provider.Namespace{"ocel-dev", "ocel-shop", "ocelot"} {
		other := Names{namespace: namespace, project: "acme-prod"}.AppBucket("shop", "prod", "uploads")
		if strings.HasPrefix(other, prefix) {
			t.Errorf("namespace %s names its bucket %q, under namespace ocel's prefix %q, so ocel's deploy credential administers it", namespace, other, prefix)
		}
	}
	if own := (Names{namespace: "ocel", project: "acme-prod"}).AppBucket("dev", "prod", "uploads"); !strings.HasPrefix(own, prefix) {
		t.Errorf("AppBucket() = %q, want it under the namespace's prefix %q", own, prefix)
	}
}

func TestAnAppBucketWhoseReadableNameRunsLongIsCutToWhatCloudStorageTakes(t *testing.T) {
	names := Names{namespace: "ocel", project: "acme-prod"}

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

	binding, err := p.ProvisionBucket(context.Background(), aBucket(provider.BucketSpec{}), nil)
	if err != nil {
		t.Fatalf("ProvisionBucket() = %v", err)
	}

	name := names(t, p).AppBucket("shop", "prod", "uploads")
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
		t.Fatalf("created lifecycle %s, want multipart uploads abandoned for a day aborted, or their parts bill forever", asJSON(t, created.Lifecycle))
	}
	if !slices.ContainsFunc(created.Lifecycle.Rule, func(rule *raw.BucketLifecycleRule) bool {
		return rule.Action.Type == "Delete" && slices.Equal(rule.Condition.MatchesPrefix, []string{s3store.SessionKeyPrefix})
	}) {
		t.Errorf("created lifecycle %s, want the upload sessions the runtime keeps deleted once they have long expired", asJSON(t, created.Lifecycle))
	}
	if policy := server.granted(name); policy != nil && len(policy.Bindings) != 0 {
		t.Errorf("a freshly provisioned bucket is bound to %s, want no account: an app is granted the buckets it binds when it deploys", asJSON(t, policy.Bindings))
	}
	if binding.Type != provider.BindingBucket || binding.Name != "uploads" || binding.Properties[provider.PropertyBucket] != name {
		t.Errorf("ProvisionBucket() = %+v, want a bucket binding for uploads naming %s", binding, name)
	}
}

func TestABucketAllowsBrowserUploadsFromTheOriginsItDeclares(t *testing.T) {
	p, server := servingStorage(t)

	if _, err := p.ProvisionBucket(context.Background(), aBucket(provider.BucketSpec{AllowedOrigins: []string{"https://shop.example"}}), nil); err != nil {
		t.Fatalf("ProvisionBucket() = %v", err)
	}

	cors := server.created[0].Cors
	if len(cors) != 1 || !slices.Equal(cors[0].Origin, []string{"https://shop.example"}) ||
		!slices.Contains(cors[0].Method, http.MethodPut) || !slices.Contains(cors[0].Method, http.MethodPost) ||
		!slices.Contains(cors[0].ResponseHeader, "ETag") {
		t.Errorf("created CORS %s, want PUT and POST from https://shop.example with ETag readable, as a multipart upload needs", asJSON(t, cors))
	}
}

func TestABucketAlreadyInPlaceIsMendedOnlyWhereItDrifted(t *testing.T) {
	p, server := servingStorage(t)
	if _, err := p.ProvisionBucket(context.Background(), aBucket(provider.BucketSpec{}), nil); err != nil {
		t.Fatal(err)
	}

	if _, err := p.ProvisionBucket(context.Background(), aBucket(provider.BucketSpec{}), nil); err != nil {
		t.Fatalf("ProvisionBucket() = %v", err)
	}
	if len(server.created) != 1 || len(server.patched) != 0 || len(server.setters) != 0 {
		t.Errorf("a second deploy created %d, patched %d and set %d policies, want nothing written over a bucket already as declared",
			len(server.created)-1, len(server.patched), len(server.setters))
	}

	if _, err := p.ProvisionBucket(context.Background(), aBucket(provider.BucketSpec{AllowedOrigins: []string{"https://shop.example"}}), nil); err != nil {
		t.Fatalf("ProvisionBucket() = %v", err)
	}
	if len(server.patched) != 1 || len(server.patched[0].Cors) != 1 {
		t.Errorf("declaring an origin patched %s, want the bucket's CORS patched to it", asJSON(t, server.patched))
	}
}

func TestABucketNameAnotherAccountHoldsIsRefusedWithWhatToRename(t *testing.T) {
	p, server := servingStorage(t)
	server.takenElsewhere = true

	_, err := p.ProvisionBucket(context.Background(), aBucket(provider.BucketSpec{}), nil)
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid || !strings.Contains(err.Error(), "uploads") {
		t.Fatalf("ProvisionBucket() over a name taken elsewhere = %v, want an %s refusal naming the bucket to rename", err, refusal.CodeInvalid)
	}
}

func TestRemovingABucketDeletesItsObjectsThenTheBucket(t *testing.T) {
	p, server := servingStorage(t)
	name := names(t, p).AppBucket("shop", "prod", "uploads")
	server.holding(&raw.Bucket{Name: name}, "avatars/one.png", s3store.SessionKeyPrefix+"sess_1")

	binding := provider.Binding{Type: provider.BindingBucket, Name: "uploads", Properties: map[string]string{provider.PropertyBucket: name}}
	if err := p.RemoveResource(context.Background(), aBucket(provider.BucketSpec{}).Ref, binding, nil); err != nil {
		t.Fatalf("RemoveResource() = %v", err)
	}
	if len(server.removed) != 2 || !slices.Equal(server.deleted, []string{name}) {
		t.Errorf("removal deleted objects %v and buckets %v, want every object and then %s", server.removed, server.deleted, name)
	}

	if err := p.RemoveResource(context.Background(), aBucket(provider.BucketSpec{}).Ref, binding, nil); err != nil {
		t.Errorf("RemoveResource() of a bucket already gone = %v, want nothing to do", err)
	}
}

func TestABootstrapChecksTheAPIAppsSignTheirBucketURLsThroughIsOn(t *testing.T) {
	t.Parallel()

	if !slices.Contains(apisFor(environment.TierProduction, nil), "iamcredentials.googleapis.com") {
		t.Errorf("a bootstrap checks %v are on, want iamcredentials.googleapis.com among them: an app signs its buckets' URLs through it, and every signed URL fails while it is off", apisFor(environment.TierProduction, nil))
	}
}

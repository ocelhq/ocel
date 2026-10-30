package gcp

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	costv1 "github.com/ocelhq/ocel/pkg/proto/provider/cost/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/transform"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/control"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
)

type patching struct {
	patches transform.Patches
	tags    map[string]string
	seen    transform.Request
}

func (p *patching) Evaluate(_ context.Context, req transform.Request) ([]transform.Result, error) {
	p.seen = req
	results := make([]transform.Result, len(req.Resources))
	for i := range results {
		results[i] = transform.Result{Patches: overTheWire(p.patches), Tags: p.tags}
	}
	return results, nil
}

func overTheWire(patches transform.Patches) transform.Patches {
	encoded, err := json.Marshal(patches)
	if err != nil {
		panic(err)
	}
	var copied transform.Patches
	if err := json.Unmarshal(encoded, &copied); err != nil {
		panic(err)
	}
	return copied
}

type publishedRecords []provider.Binding

func (p publishedRecords) Names(context.Context) ([]string, error) {
	var names []string
	for _, record := range p {
		names = append(names, record.Name)
	}
	return names, nil
}

func (p publishedRecords) Published(context.Context) ([]provider.Binding, error) { return p, nil }

func (p publishedRecords) Named(_ context.Context, name string) (provider.Binding, error) {
	for _, record := range p {
		if record.Name == name {
			return record, nil
		}
	}
	return provider.Binding{}, refusal.Refuse(refusal.CodeInvalid, "nothing published %s", name)
}

func bucketStack() provider.StackSpec {
	return provider.StackSpec{
		Ref:       provider.StackRef{Project: "shop", Tier: environment.TierProduction, Name: naming.InfraStack("prod")},
		Kind:      provider.StackInfra,
		Resources: []provider.Resource{{Name: "uploads", Declared: "uploads", Type: provider.BindingBucket, Bucket: &provider.BucketSpec{}}},
	}
}

func transforming(t *testing.T, pass transform.Pass) (*Provider, *storageServer) {
	t.Helper()
	p, server := servingStorage(t)
	p.transforms = pass
	return p, server
}

func provisionBucketStack(t *testing.T, p *Provider, spec provider.StackSpec) {
	t.Helper()
	if _, err := p.Stacks().Provision(context.Background(), spec, &fake.Log{}); err != nil {
		t.Fatalf("Provision() = %v", err)
	}
}

func TestATransformIsAskedAboutTheBucketsAStackProvisions(t *testing.T) {
	pass := &patching{}
	p, _ := transforming(t, pass)

	provisionBucketStack(t, p, bucketStack())

	if pass.seen.Provider != "gcp" || pass.seen.EnvTier != "production" || pass.seen.Env != "prod" {
		t.Errorf("the transform was told %q in %q/%q, want the gcp branch in production/prod", pass.seen.Provider, pass.seen.EnvTier, pass.seen.Env)
	}
	if !slices.Equal(pass.seen.Resources, []transform.Resource{{Type: "bucket", Name: "uploads"}}) {
		t.Errorf("the transform was offered %v, want the bucket being provisioned", pass.seen.Resources)
	}
}

func TestATransformedBucketIsCreatedWithWhatThePatchSet(t *testing.T) {
	p, server := transforming(t, &patching{patches: transform.Patches{"bucket": {
		"storageClass":     "NEARLINE",
		"versioning":       map[string]any{"enabled": true},
		"softDeletePolicy": map[string]any{"retentionDurationSeconds": 0},
		"logging":          map[string]any{"logBucket": "acme-logs"},
		"lifecycleRules": []any{map[string]any{
			"action":    map[string]any{"type": "SetStorageClass", "storageClass": "COLDLINE"},
			"condition": map[string]any{"age": 90},
		}},
	}}, tags: map[string]string{"team": "storage"}})

	provisionBucketStack(t, p, bucketStack())

	created := server.created[0]
	if created.StorageClass != "NEARLINE" || created.Versioning == nil || !created.Versioning.Enabled {
		t.Errorf("created storage class %q and versioning %s, want NEARLINE with versioning on", created.StorageClass, googletest.JSON(t, created.Versioning))
	}
	if created.SoftDeletePolicy == nil || created.SoftDeletePolicy.RetentionDurationSeconds != 0 {
		t.Errorf("created soft delete %s, want it off", googletest.JSON(t, created.SoftDeletePolicy))
	}
	if created.Logging == nil || created.Logging.LogBucket != "acme-logs" {
		t.Errorf("created logging %s, want access logs written to acme-logs", googletest.JSON(t, created.Logging))
	}
	rules := created.Lifecycle.Rule
	if len(rules) != 3 || rules[2].Action.Type != "SetStorageClass" || rules[2].Action.StorageClass != "COLDLINE" || rules[0].Action.Type != "AbortIncompleteMultipartUpload" {
		t.Errorf("created lifecycle %s, want the patch's rule after the two ocel keeps", googletest.JSON(t, created.Lifecycle))
	}
	if created.Labels["team"] != "storage" || created.Labels["ocel-bucket"] != "uploads" {
		t.Errorf("created labels %v, want the rule's tag beside ocel's own", created.Labels)
	}

	provisionBucketStack(t, p, bucketStack())
	if len(server.patched) != 0 {
		t.Errorf("a second deploy patched %s, want nothing sent over a bucket already as patched", googletest.JSON(t, server.patched))
	}
}

func TestAPatchAddedToABucketInPlaceIsPlannedAndSent(t *testing.T) {
	pass := &patching{}
	p, server := transforming(t, pass)
	provisionBucketStack(t, p, bucketStack())

	pass.patches = transform.Patches{"bucket": {"storageClass": "COLDLINE", "versioning": map[string]any{"enabled": true}}}
	plan, err := p.Stacks().Plan(context.Background(), bucketStack(), nil)
	if err != nil {
		t.Fatalf("Plan() = %v", err)
	}
	if len(plan.Groups) != 1 || len(plan.Groups[0].Changes) != 1 {
		t.Fatalf("Plan() = %+v, want the one bucket", plan.Groups)
	}
	if change := plan.Groups[0].Changes[0]; change.Action != provider.ActionUpdate ||
		!strings.Contains(change.Reason, "storage class changes from STANDARD to COLDLINE") || !strings.Contains(change.Reason, "versioning changes from off to on") {
		t.Fatalf("Plan() = %+v, want the bucket updated for its storage class and versioning", change)
	}

	provisionBucketStack(t, p, bucketStack())
	if len(server.patched) != 1 || server.patched[0].StorageClass != "COLDLINE" || server.patched[0].Versioning == nil || !server.patched[0].Versioning.Enabled {
		t.Errorf("patched %s, want the storage class and versioning sent", googletest.JSON(t, server.patched))
	}
}

func TestRemovingAPatchPutsTheBucketBackToWhatOcelSets(t *testing.T) {
	pass := &patching{
		patches: transform.Patches{"bucket": {
			"storageClass":     "NEARLINE",
			"versioning":       map[string]any{"enabled": true},
			"softDeletePolicy": map[string]any{"retentionDurationSeconds": 0},
			"retentionPolicy":  map[string]any{"retentionPeriod": "86400"},
			"encryption":       map[string]any{"defaultKmsKeyName": "projects/acme/locations/europe-west1/keyRings/r/cryptoKeys/k"},
			"logging":          map[string]any{"logBucket": "acme-logs"},
			"lifecycleRules":   []any{map[string]any{"action": map[string]any{"type": "Delete"}, "condition": map[string]any{"age": 30}}},
		}},
		tags: map[string]string{"team": "storage"},
	}
	p, server := transforming(t, pass)
	provisionBucketStack(t, p, bucketStack())

	pass.patches, pass.tags = nil, nil
	provisionBucketStack(t, p, bucketStack())

	if len(server.patched) != 1 {
		t.Fatalf("patched %d times, want the bucket put back once", len(server.patched))
	}
	sent := server.patched[0]
	if sent.StorageClass != "STANDARD" || sent.Versioning == nil || sent.Versioning.Enabled {
		t.Errorf("sent storage class %q and versioning %s, want STANDARD with versioning off", sent.StorageClass, googletest.JSON(t, sent.Versioning))
	}
	if sent.SoftDeletePolicy == nil || sent.SoftDeletePolicy.RetentionDurationSeconds != 7*24*60*60 {
		t.Errorf("sent soft delete %s, want Google's default of seven days back", googletest.JSON(t, sent.SoftDeletePolicy))
	}
	for _, cleared := range []string{"retentionPolicy", "logging", "encryption.defaultKmsKeyName", "labels.team"} {
		if !slices.Contains(server.cleared, cleared) {
			t.Errorf("cleared %v, want %s cleared to Google's default", server.cleared, cleared)
		}
	}
	if rules := sent.Lifecycle; rules == nil || len(rules.Rule) != 2 {
		t.Errorf("sent lifecycle %s, want ocel's two rules alone", googletest.JSON(t, rules))
	}

	provisionBucketStack(t, p, bucketStack())
	if len(server.patched) != 1 {
		t.Errorf("patched %d times, want nothing sent once the bucket is back as ocel sets it", len(server.patched))
	}
}

func TestRemovingALogPrefixPutsBackGooglesPrefixOfTheBucketsName(t *testing.T) {
	pass := &patching{patches: transform.Patches{"bucket": {"logging": map[string]any{"logBucket": "acme-logs", "logObjectPrefix": "uploads/"}}}}
	p, server := transforming(t, pass)
	provisionBucketStack(t, p, bucketStack())

	pass.patches = transform.Patches{"bucket": {"logging": map[string]any{"logBucket": "acme-logs"}}}
	provisionBucketStack(t, p, bucketStack())
	provisionBucketStack(t, p, bucketStack())

	if len(server.patched) != 1 || server.patched[0].Logging == nil || server.patched[0].Logging.LogObjectPrefix != server.created[0].Name {
		t.Errorf("patched %s, want the prefix sent back once as the bucket's own name", googletest.JSON(t, server.patched))
	}
}

func TestABucketPatchIsFilledFromTheRecordItNames(t *testing.T) {
	key := "projects/acme/locations/europe-west1/keyRings/r/cryptoKeys/uploads"
	p, server := transforming(t, &patching{patches: transform.Patches{"bucket": {
		"encryption": map[string]any{"defaultKmsKeyName": map[string]any{"$ocelOutput": map[string]any{"type": "custom", "name": "keys", "property": "bucketKey"}}},
	}}})
	spec := bucketStack()
	spec.Bindings = publishedRecords{{Name: "keys", Type: provider.BindingCustom, Properties: map[string]string{"bucketKey": key}}}

	provisionBucketStack(t, p, spec)

	if created := server.created[0]; created.Encryption == nil || created.Encryption.DefaultKmsKeyName != key {
		t.Errorf("created encryption %s, want the key the custom record names", googletest.JSON(t, created.Encryption))
	}
}

func TestABucketPatchToAFieldOcelSetsIsRefusedBeforeTheBucketIsReached(t *testing.T) {
	p, server := transforming(t, &patching{patches: transform.Patches{"bucket": {"publicAccessPrevention": "inherited"}}})

	_, err := p.Stacks().Provision(context.Background(), bucketStack(), &fake.Log{})

	if err == nil || !strings.Contains(err.Error(), "bucket.publicAccessPrevention") {
		t.Errorf("Provision() = %v, want the patch refused by name", err)
	}
	if len(server.writes) != 0 {
		t.Errorf("wrote %v, want Cloud Storage untouched", server.writes)
	}
}

func TestRemovingABucketForgetsWhatItsTransformsPatched(t *testing.T) {
	p, _ := transforming(t, &patching{patches: transform.Patches{"bucket": {"storageClass": "NEARLINE"}}})
	spec := bucketStack()
	result, err := p.Stacks().Provision(context.Background(), spec, &fake.Log{})
	if err != nil {
		t.Fatal(err)
	}

	if err := p.bucketHooks().Remove(context.Background(), spec.Ref, result.Bindings[0], &fake.Log{}); err != nil {
		t.Fatalf("Remove() = %v", err)
	}

	for key := range p.Records.(*fake.KeyValues).Snapshot() {
		if strings.Contains(key, "patches") {
			t.Errorf("%s is left behind, want every record a bucket's patches wrote removed with it", key)
		}
	}
}

func inventoriedBucket(t *testing.T, pass transform.Pass) *costv1.Resource {
	t.Helper()
	p := pushing(t, "")
	p.transforms = pass
	set, err := p.InventoryDeploy(context.Background(), provider.InventoryRequest{
		Deploy:    provider.DeploySpec{Slug: "shop", Tier: environment.TierProduction, Env: "prod"},
		Resources: bucketStack().Resources,
	})
	if err != nil {
		t.Fatalf("InventoryDeploy() = %v", err)
	}
	for _, resource := range set.GetResources() {
		if resource.GetType() == "google_storage_bucket" && strings.Contains(resource.GetName(), "uploads") {
			return resource
		}
	}
	t.Fatalf("InventoryDeploy() inventoried %v, want the uploads bucket", set.GetResources())
	return nil
}

func TestABucketIsPricedAtTheStorageClassItsTransformSets(t *testing.T) {
	inventoried := inventoriedBucket(t, &patching{patches: transform.Patches{"bucket": {"storageClass": "ARCHIVE", "versioning": map[string]any{"enabled": true}}}})

	properties := inventoried.GetProperties().AsMap()
	if properties["storage_class"] != "ARCHIVE" || properties["versioning"].(map[string]any)["enabled"] != true {
		t.Errorf("the bucket is priced as %v, want the ARCHIVE class with versioning the transform sets", properties)
	}
}

func TestABucketWhoseStorageClassABindingFillsIsPricedAsUnknown(t *testing.T) {
	inventoried := inventoriedBucket(t, &patching{patches: transform.Patches{"bucket": {
		"storageClass": map[string]any{"$ocelOutput": map[string]any{"type": "custom", "name": "tiers", "property": "class"}},
	}}})

	if !slices.Contains(inventoried.GetUnknown(), "storage_class") {
		t.Errorf("the bucket is priced with unknown %v, want its storage class unknown until the deploy reads the record", inventoried.GetUnknown())
	}
}

func TestAProjectListingTransformsIsServedOnGoogleCloud(t *testing.T) {
	if !newProvider(&control.Provider{}).Facts().RendersTransforms {
		t.Error("a project listing a transform is refused on Google Cloud, which now patches what it provisions")
	}
}

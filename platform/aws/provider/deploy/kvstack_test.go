package deploy

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	sdk "github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/transform"
)

const conformanceKVTokenRoot = "/ocel/kv"

func conformanceKVToken(name string) string {
	return kvTokenParameter(conformanceKVTokenRoot, "conformance", "conformance", name)
}

func kvOutput(name string) auto.OutputValue {
	return auto.OutputValue{Value: map[string]any{
		outputKeyHost:               "master.ocel-app-conformance-" + name + ".abc123.euw1.cache.amazonaws.com",
		outputKeyPort:               float64(6379),
		outputKeyAuthTokenParameter: conformanceKVToken(name),
	}}
}

func kvStacks(engine *mockedEngine, params *fakeParameters) *Stacks {
	cfg := conformingConfig(&fakeArtifactStore{})
	cfg.Parameters = params
	return stacksWith(cfg, engine)
}

var conformanceInfra = provider.StackRef{Project: "conformance", Tier: environment.TierProduction, Name: naming.InfraStack("conformance")}

func TestProvisioningAStoreMintsItsTokenAndBindsTheStoreToIt(t *testing.T) {
	t.Parallel()

	params := newFakeParameters()
	engine := &mockedEngine{outputs: auto.OutputMap{"c-kv": kvOutput("c-kv")}}
	result, err := kvStacks(engine, params).Provision(context.Background(), provider.StackSpec{
		Ref:       conformanceInfra,
		Kind:      provider.StackInfra,
		Resources: []provider.Resource{{Name: "c-kv", Type: provider.BindingKV, KV: &provider.KVSpec{}}},
	}, nil)
	if err != nil {
		t.Fatalf("Provision() = %v", err)
	}

	token, minted := params.values[conformanceKVToken("c-kv")]
	if !minted || !mintedToken.MatchString(token) {
		t.Fatalf("the store's token parameter holds %q, want a minted token", token)
	}
	if len(result.Bindings) != 1 {
		t.Fatalf("Provision() returned %d bindings, want the store's", len(result.Bindings))
	}
	got := result.Bindings[0]
	if got.Type != provider.BindingKV || got.Properties[provider.PropertyPassword] != token || got.Properties[provider.PropertyTLS] != "true" {
		t.Errorf("the store's binding = %+v, want a kv binding over TLS with the minted token", got)
	}
}

func TestAStoreRemovedFromTheStackHasItsTokenDeletedOnceTheDeployLands(t *testing.T) {
	t.Parallel()

	params := newFakeParameters()
	params.values[conformanceKVToken("c-kv")] = "the-token"
	params.values[conformanceKVToken("c-kept")] = "the-kept-token"
	engine := &mockedEngine{outputs: auto.OutputMap{
		"c-kv":     kvOutput("c-kv"),
		"c-kept":   kvOutput("c-kept"),
		"c-bucket": auto.OutputValue{Value: map[string]any{outputKeyBucket: "conformance-uploads"}},
	}}
	if _, err := kvStacks(engine, params).Provision(context.Background(), provider.StackSpec{
		Ref:  conformanceInfra,
		Kind: provider.StackInfra,
		Resources: []provider.Resource{
			{Name: "c-kept", Type: provider.BindingKV, KV: &provider.KVSpec{}},
			{Name: "c-bucket", Type: provider.BindingBucket, Bucket: &provider.BucketSpec{}},
		},
	}, nil); err != nil {
		t.Fatalf("Provision() = %v", err)
	}

	if !slices.Equal(params.deleted, []string{conformanceKVToken("c-kv")}) {
		t.Errorf("the deploy deleted %v, want the token of the store it removed and no other", params.deleted)
	}
}

func TestAStoreWhoseDeployFailsKeepsEveryToken(t *testing.T) {
	t.Parallel()

	params := newFakeParameters()
	params.values[conformanceKVToken("c-kv")] = "the-token"
	engine := &mockedEngine{
		outputs: auto.OutputMap{"c-kv": kvOutput("c-kv")},
		upErr:   func(string) error { return context.DeadlineExceeded },
	}
	if _, err := kvStacks(engine, params).Provision(context.Background(), provider.StackSpec{
		Ref:       conformanceInfra,
		Kind:      provider.StackInfra,
		Resources: []provider.Resource{{Name: "c-bucket", Type: provider.BindingBucket, Bucket: &provider.BucketSpec{}}},
	}, nil); err == nil {
		t.Fatal("Provision() succeeded, want the engine's failure")
	}
	if len(params.deleted) != 0 {
		t.Errorf("a failed deploy deleted %v, and the store it failed to remove still demands that token", params.deleted)
	}
}

func TestDestroyingAStackDeletesTheTokensOfItsStores(t *testing.T) {
	t.Parallel()

	params := newFakeParameters()
	params.values[conformanceKVToken("c-kv")] = "the-token"
	engine := &mockedEngine{outputs: auto.OutputMap{
		"c-kv":     kvOutput("c-kv"),
		"c-bucket": auto.OutputValue{Value: map[string]any{outputKeyBucket: "conformance-uploads"}},
	}}
	if err := kvStacks(engine, params).Destroy(context.Background(), conformanceInfra, nil); err != nil {
		t.Fatalf("Destroy() = %v", err)
	}
	if !slices.Equal(params.deleted, []string{conformanceKVToken("c-kv")}) {
		t.Errorf("Destroy() deleted %v, want the token of the stack's store", params.deleted)
	}
}

func TestATokenLeftByAFailedFirstDeployIsDeletedByTheNextDeployThatDropsTheStore(t *testing.T) {
	t.Parallel()

	params := newFakeParameters()
	params.values[conformanceKVToken("c-kv")] = "minted-before-the-deploy-failed"
	params.values[conformanceKVToken("c-kept")] = "the-kept-token"
	otherEnv := kvTokenParameter(conformanceKVTokenRoot, "conformance", "staging", "c-kv")
	params.values[otherEnv] = "another-environments-token"
	engine := &mockedEngine{outputs: auto.OutputMap{
		"c-kept":   kvOutput("c-kept"),
		"c-bucket": auto.OutputValue{Value: map[string]any{outputKeyBucket: "conformance-uploads"}},
	}}
	if _, err := kvStacks(engine, params).Provision(context.Background(), provider.StackSpec{
		Ref:  conformanceInfra,
		Kind: provider.StackInfra,
		Resources: []provider.Resource{
			{Name: "c-kept", Type: provider.BindingKV, KV: &provider.KVSpec{}},
			{Name: "c-bucket", Type: provider.BindingBucket, Bucket: &provider.BucketSpec{}},
		},
	}, nil); err != nil {
		t.Fatalf("Provision() = %v", err)
	}

	if !slices.Equal(params.deleted, []string{conformanceKVToken("c-kv")}) {
		t.Errorf("the deploy deleted %v, want the token no declared store uses, though no stack output ever named it", params.deleted)
	}
}

func TestDestroyingAStackDeletesEveryTokenUnderItsEnvironmentAndNoOther(t *testing.T) {
	t.Parallel()

	params := newFakeParameters()
	var want []string
	for i := range 12 {
		name := conformanceKVToken("c-kv-" + strconv.Itoa(i))
		params.values[name] = "a-token"
		want = append(want, name)
	}
	slices.Sort(want)
	otherEnv := kvTokenParameter(conformanceKVTokenRoot, "conformance", "staging", "c-kv")
	params.values[otherEnv] = "another-environments-token"
	engine := &mockedEngine{outputs: auto.OutputMap{}}
	if err := kvStacks(engine, params).Destroy(context.Background(), conformanceInfra, nil); err != nil {
		t.Fatalf("Destroy() = %v", err)
	}
	deleted := slices.Sorted(slices.Values(params.deleted))
	if !slices.Equal(deleted, want) {
		t.Errorf("Destroy() deleted %v, want every token under the environment, the ones no failed deploy recorded among them", deleted)
	}
	if _, kept := params.values[otherEnv]; !kept {
		t.Error("Destroy() deleted another environment's token")
	}
}

func provisionPatchedKV(t *testing.T, patch map[string]any) error {
	t.Helper()
	cfg := conformingConfig(&fakeArtifactStore{})
	cfg.Transform = patchingPass{patch: func(patches []transform.Patches) {
		patches[0]["replicationGroup"] = patch
	}}
	_, err := stacksWith(cfg, &mockedEngine{outputs: auto.OutputMap{"c-kv": kvOutput("c-kv")}}).Provision(context.Background(), provider.StackSpec{
		Ref:       conformanceInfra,
		Kind:      provider.StackInfra,
		Resources: []provider.Resource{{Name: "c-kv", Type: provider.BindingKV, KV: &provider.KVSpec{}}},
	}, nil)
	return err
}

func TestATransformDropsAStoreToOneNodeWithNoFailover(t *testing.T) {
	t.Parallel()

	pass := patchingPass{patch: func(patches []transform.Patches) {
		patches[0]["replicationGroup"] = map[string]any{
			"numCacheClusters":         1,
			"automaticFailoverEnabled": false,
			"multiAzEnabled":           false,
		}
	}}
	transformed, err := transformStackSpec(context.Background(), pass, provider.StackSpec{
		Ref:       conformanceInfra,
		Kind:      provider.StackInfra,
		Resources: []provider.Resource{{Name: "c-kv", Type: provider.BindingKV, KV: &provider.KVSpec{}}},
	})
	if err != nil {
		t.Fatalf("transformStackSpec() = %v", err)
	}

	rendered := sdk.Map{}
	for field, value := range registeredKV(t, "shop").inputsOf(t, tokenElastiCacheReplicationGroup, "kv-cache").Mappable() {
		switch scalar := value.(type) {
		case string:
			rendered[field] = sdk.String(scalar)
		case float64:
			rendered[field] = sdk.Float64(scalar)
		case bool:
			rendered[field] = sdk.Bool(scalar)
		}
	}
	transformed.claimed = map[resourceRef]bool{}
	merged, claimed := transformed.claim(resourceRef{Token: tokenElastiCacheReplicationGroup, Name: "kv-c-kv"}, rendered)
	if !claimed {
		t.Fatal("the patch reached no replication group ocel constructs")
	}
	for field, want := range map[string]any{
		"numCacheClusters":         float64(1),
		"automaticFailoverEnabled": false,
		"multiAzEnabled":           false,
		"nodeType":                 "cache.t4g.micro",
		"transitEncryptionEnabled": true,
	} {
		if got := mergedValue(t, merged[field]); !sameValue(got, want) {
			t.Errorf("the patched replication group's %s = %#v, want %v", field, got, want)
		}
	}
}

func TestATransformDroppingAStoreToOneNodeWithFailoverStillOnIsRefused(t *testing.T) {
	t.Parallel()

	for _, patch := range []map[string]any{
		{"numCacheClusters": 1},
		{"numCacheClusters": 1, "automaticFailoverEnabled": false},
		{"numCacheClusters": 1, "multiAzEnabled": false},
		{"numCacheClusters": 1, "automaticFailoverEnabled": true, "multiAzEnabled": false},
	} {
		err := provisionPatchedKV(t, patch)
		if err == nil || !strings.Contains(err.Error(), "c-kv") || !strings.Contains(err.Error(), "automaticFailoverEnabled") || !strings.Contains(err.Error(), "multiAzEnabled") {
			t.Errorf("Provision() with %v = %v, want it refused naming the store and both flags ElastiCache refuses on one node", patch, err)
		}
	}
}

func TestPreviewingAStoreWritesNoToken(t *testing.T) {
	t.Parallel()

	params := newFakeParameters()
	engine := &mockedEngine{outputs: auto.OutputMap{}}
	if _, err := kvStacks(engine, params).Plan(context.Background(), provider.StackSpec{
		Ref:       conformanceInfra,
		Kind:      provider.StackInfra,
		Resources: []provider.Resource{{Name: "c-kv", Type: provider.BindingKV, KV: &provider.KVSpec{}}},
	}, nil); err != nil {
		t.Fatalf("Plan() = %v", err)
	}
	if params.puts != 0 {
		t.Errorf("Plan() wrote %d parameters, want a preview to write none", params.puts)
	}
}

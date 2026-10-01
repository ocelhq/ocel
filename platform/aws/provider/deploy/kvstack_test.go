package deploy

import (
	"context"
	"slices"
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

func TestATransformDropsAStoreToOneNode(t *testing.T) {
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

	group := resourceRef{Token: tokenElastiCacheReplicationGroup, Name: "kv-c-kv"}
	transformed.claimed = map[resourceRef]bool{}
	merged, claimed := transformed.claim(group, sdk.Map{
		"numCacheClusters":         sdk.Int(kvNodeCount),
		"automaticFailoverEnabled": sdk.Bool(true),
		"multiAzEnabled":           sdk.Bool(true),
	})
	if !claimed {
		t.Fatalf("the patch named %+v, and no replication group ocel constructs claimed it", group)
	}
	for field, want := range map[string]any{"numCacheClusters": float64(1), "automaticFailoverEnabled": false, "multiAzEnabled": false} {
		if got := mergedValue(t, merged[field]); !sameValue(got, want) {
			t.Errorf("%s = %#v, want %v", field, got, want)
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

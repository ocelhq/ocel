package deploy

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"slices"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/payloads"
)

const conformanceSigningKeyRoot = "ocel/realtime"

var conformanceSeed = base64.StdEncoding.EncodeToString([]byte(strings.Repeat("s", ed25519.SeedSize)))

func conformanceSigningKey(name string) string {
	return signingKeySecret(conformanceSigningKeyRoot, "conformance", "conformance", name)
}

func realtimeOutput(name string) auto.OutputValue {
	return auto.OutputValue{Value: map[string]any{
		outputKeyHost:         "abc.appsync-api.eu-west-1.amazonaws.com",
		outputKeyRealtimeHost: "abc.appsync-realtime-api.eu-west-1.amazonaws.com",
		outputKeyAPIARN:       "arn:aws:appsync:eu-west-1:111122223333:apis/abcdefghij",
		outputKeyNamespace:    name,
		outputKeySigningKey:   conformanceSigningKey(name),
	}}
}

func realtimeStacks(engine *mockedEngine, keys *fakeSigningKeys) *Stacks {
	cfg := conformingConfig(&fakeArtifactStore{})
	cfg.SigningKeys = keys
	return stacksWith(cfg, engine)
}

func TestProvisioningARealtimeResourceMintsItsKeyAndBindsTheAppToAppSync(t *testing.T) {
	t.Parallel()

	keys := newFakeSigningKeys()
	engine := &mockedEngine{outputs: auto.OutputMap{"c-realtime": realtimeOutput("c-realtime")}}
	result, err := realtimeStacks(engine, keys).Provision(context.Background(), provider.StackSpec{
		Ref:       conformanceInfra,
		Kind:      provider.StackInfra,
		Resources: []provider.Resource{{Name: "c-realtime", Declared: "c-realtime", Type: provider.BindingRealtime, Realtime: &provider.RealtimeSpec{}}},
	}, nil)
	if err != nil {
		t.Fatalf("Provision() = %v", err)
	}
	seed, minted := keys.values[conformanceSigningKey("c-realtime")]
	if !minted {
		t.Fatalf("no signing key was minted at %s", conformanceSigningKey("c-realtime"))
	}
	if len(result.Bindings) != 1 {
		t.Fatalf("Provision() returned %d bindings, want the realtime resource's", len(result.Bindings))
	}
	got := result.Bindings[0]
	if got.Type != provider.BindingRealtime || got.Properties[provider.PropertySigningKey] != seed || got.Resource != "c-realtime" {
		t.Errorf("binding = %+v, want a realtime binding signing with the minted key", got)
	}
}

func TestARealtimeResourceRemovedFromTheStackHasItsKeyDeletedOnceTheDeployLands(t *testing.T) {
	t.Parallel()

	keys := newFakeSigningKeys()
	keys.values[conformanceSigningKey("c-realtime")] = conformanceSeed
	keys.values[conformanceSigningKey("c-kept")] = conformanceSeed
	engine := &mockedEngine{outputs: auto.OutputMap{"c-kept": realtimeOutput("c-kept")}}
	if _, err := realtimeStacks(engine, keys).Provision(context.Background(), provider.StackSpec{
		Ref:       conformanceInfra,
		Kind:      provider.StackInfra,
		Resources: []provider.Resource{{Name: "c-kept", Declared: "c-kept", Type: provider.BindingRealtime, Realtime: &provider.RealtimeSpec{}}},
	}, nil); err != nil {
		t.Fatalf("Provision() = %v", err)
	}
	if !slices.Equal(keys.deleted, []string{conformanceSigningKey("c-realtime")}) {
		t.Errorf("the deploy deleted %v, want the key of the resource it removed and no other", keys.deleted)
	}
}

func TestARealtimeDeployThatFailsKeepsEveryKey(t *testing.T) {
	t.Parallel()

	keys := newFakeSigningKeys()
	keys.values[conformanceSigningKey("c-realtime")] = conformanceSeed
	engine := &mockedEngine{outputs: auto.OutputMap{}, upErr: func(string) error { return context.DeadlineExceeded }}
	if _, err := realtimeStacks(engine, keys).Provision(context.Background(), provider.StackSpec{
		Ref:  conformanceInfra,
		Kind: provider.StackInfra,
	}, nil); err == nil {
		t.Fatal("Provision() succeeded, want the engine's failure")
	}
	if len(keys.deleted) != 0 {
		t.Errorf("a failed deploy deleted %v, and the resource it failed to remove still signs with that key", keys.deleted)
	}
}

func TestDestroyingAStackDeletesEveryRealtimeKeyOfItsEnvironmentAndNoOther(t *testing.T) {
	t.Parallel()

	keys := newFakeSigningKeys()
	keys.values[conformanceSigningKey("c-realtime")] = conformanceSeed
	keys.values[conformanceSigningKey("c-chat")] = conformanceSeed
	otherEnv := signingKeySecret(conformanceSigningKeyRoot, "conformance", "staging", "c-realtime")
	keys.values[otherEnv] = conformanceSeed
	if err := realtimeStacks(&mockedEngine{outputs: auto.OutputMap{}}, keys).Destroy(context.Background(), conformanceInfra, nil); err != nil {
		t.Fatalf("Destroy() = %v", err)
	}
	want := []string{conformanceSigningKey("c-chat"), conformanceSigningKey("c-realtime")}
	if deleted := slices.Sorted(slices.Values(keys.deleted)); !slices.Equal(deleted, want) {
		t.Errorf("Destroy() deleted %v, want %v", deleted, want)
	}
	if _, kept := keys.values[otherEnv]; !kept {
		t.Error("Destroy() deleted another environment's key")
	}
}

func TestProvisioningRealtimePlacesTheAuthorizerItDeclares(t *testing.T) {
	t.Parallel()

	uploader := &fakeArtifactStore{}
	recorder := &lambdaCodeRecorder{}
	engine := &mockedEngine{outputs: provisionedOutputs(), mocks: recorder}
	if _, err := stacksPlacingInto(engine, uploader).Provision(context.Background(), provider.StackSpec{
		Ref:       provider.StackRef{Project: "conformance", Tier: environment.TierProduction, Name: naming.InfraStack("conformance")},
		Kind:      provider.StackInfra,
		Resources: []provider.Resource{{Name: "c-realtime", Declared: "c-realtime", Type: provider.BindingRealtime, Realtime: &provider.RealtimeSpec{}}},
	}, nil); err != nil {
		t.Fatalf("Provision() = %v", err)
	}
	at := payloads.At("ocel-artifacts", realtimeAuthorizerKeyPrefix, payloads.RealtimeAuthorizer())
	if !slices.Contains(uploader.puts, at.Key) {
		t.Errorf("uploaded %v, want the realtime authorizer placed at %s", uploader.puts, at.Key)
	}
	want := payloads.Placement{Bucket: at.Bucket, Key: at.Key}
	if placed := recorder.code["realtime-authorizer"]; placed != want {
		t.Errorf("the authorizer lambda declares code at %+v, want %+v", placed, want)
	}
}

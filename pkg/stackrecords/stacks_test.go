package stackrecords_test

import (
	"bytes"
	"context"
	"maps"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

func TestAStackRecordHoldsNoSecretOfTheBindingsItNames(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewKeyValues()
	name := naming.InfraStack("main")
	secrets := []string{"pg-password-s3cret", "kv-password-s3cret", "signing-key-s3cret", "bucket-secret-access-key-s3cret", "custom-token-s3cret"}

	err := stackrecords.Write(ctx, store, environment.TierProduction, "shop", name, stackrecords.Stack{
		Kind: provider.StackInfra,
		Bindings: []provider.Binding{
			{Type: provider.BindingPostgres, Name: "db", Properties: map[string]string{
				provider.PropertyHost: "db.internal", provider.PropertyPort: "5432",
				provider.PropertyDatabase: "app", provider.PropertyUsername: "admin",
				provider.PropertyPassword: secrets[0],
			}},
			{Type: provider.BindingKV, Name: "cache", Properties: map[string]string{
				provider.PropertyHost: "cache.internal", provider.PropertyPassword: secrets[1],
			}},
			{Type: provider.BindingRealtime, Name: "live", Properties: map[string]string{
				provider.PropertyURL: "/socket", provider.PropertySigningKey: secrets[2],
				provider.PropertyVerifyKey: "verify",
			}},
			{Type: provider.BindingBucket, Name: "uploads", Properties: map[string]string{
				provider.PropertyBucket: "uploads", provider.PropertyEndpoint: "https://s3.internal",
				"accessKeyId": "AKIA", "secretAccessKey": secrets[3],
			}},
			{Type: provider.BindingCustom, Name: "stripe", Properties: map[string]string{"token": secrets[4]}},
		},
	})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	raw, err := keyvalue.ReadOrEmpty(ctx, store, stackrecords.StackKey(environment.TierProduction, "shop", name))
	if err != nil {
		t.Fatalf("read the raw record: %v", err)
	}
	for _, secret := range secrets {
		if bytes.Contains(raw.Value, []byte(secret)) {
			t.Errorf("the stored stack record holds %q, and the record store is readable without the sealer", secret)
		}
	}

	read, found, err := stackrecords.Read(ctx, store, environment.TierProduction, "shop", name)
	if err != nil || !found {
		t.Fatalf("Read = found %v, err %v", found, err)
	}
	db := read.Bindings[0]
	if db.Properties[provider.PropertyHost] != "db.internal" || db.Properties[provider.PropertyPort] != "5432" {
		t.Errorf("Read's postgres binding = %v, want host and port kept: teardown and port forwarding reach the resource by them", db.Properties)
	}
	if read.Bindings[2].Properties[provider.PropertyVerifyKey] != "verify" {
		t.Errorf("Read's realtime binding = %v, want the public verify key kept", read.Bindings[2].Properties)
	}
	if read.Bindings[3].Properties[provider.PropertyBucket] != "uploads" {
		t.Errorf("Read's bucket binding = %v, want the bucket kept: bucket removal empties it by name", read.Bindings[3].Properties)
	}
}

func TestWritingAStackRecordLeavesTheCallersBindingsWhole(t *testing.T) {
	t.Parallel()
	bindings := []provider.Binding{{Type: provider.BindingPostgres, Name: "db", Properties: map[string]string{
		provider.PropertyPassword: "pg-password-s3cret",
	}}}
	err := stackrecords.Write(context.Background(), fake.NewKeyValues(), environment.TierProduction, "shop",
		naming.InfraStack("main"), stackrecords.Stack{Kind: provider.StackInfra, Bindings: bindings})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if bindings[0].Properties[provider.PropertyPassword] != "pg-password-s3cret" {
		t.Errorf("Write left the caller's binding with %v, and the deploy still publishes that binding after recording it", bindings[0].Properties)
	}
}

func TestAStackRecordKeepsOnlyTheBindingPropertiesItKnowsAreNotSecret(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewKeyValues()
	name := naming.InfraStack("main")

	err := stackrecords.Write(ctx, store, environment.TierProduction, "shop", name, stackrecords.Stack{
		Kind: provider.StackInfra,
		Bindings: []provider.Binding{
			{Type: provider.BindingPostgres, Name: "db", Properties: map[string]string{
				provider.PropertyHost: "db.internal", "adminToken": "admin-token-s3cret",
			}},
			{Type: provider.BindingTopic, Name: "orders", Properties: map[string]string{
				stackrecords.PropertyDeclared: "orders", stackrecords.PropertyTopicSpec: `{"ordered":true}`,
				"topic": "projects/p/topics/orders",
			}},
			{Type: provider.BindingBucket, Name: "uploads", Properties: map[string]string{
				provider.PropertyBucket: "uploads", stackrecords.PropertySweepUploads: "true",
				stackrecords.PropertyAllowedOrigins: "https://shop.example",
			}},
		},
	})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	raw, err := keyvalue.ReadOrEmpty(ctx, store, stackrecords.StackKey(environment.TierProduction, "shop", name))
	if err != nil {
		t.Fatalf("read the raw record: %v", err)
	}
	for _, unclassified := range []string{"admin-token-s3cret", "projects/p/topics/orders"} {
		if bytes.Contains(raw.Value, []byte(unclassified)) {
			t.Errorf("the stored stack record holds %q, a property neither bindings.proto nor the record declares, so it may be a secret", unclassified)
		}
	}
	read, _, err := stackrecords.Read(ctx, store, environment.TierProduction, "shop", name)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	want := []map[string]string{
		{provider.PropertyHost: "db.internal"},
		{stackrecords.PropertyDeclared: "orders", stackrecords.PropertyTopicSpec: `{"ordered":true}`},
		{provider.PropertyBucket: "uploads", stackrecords.PropertySweepUploads: "true", stackrecords.PropertyAllowedOrigins: "https://shop.example"},
	}
	for i, binding := range read.Bindings {
		if !maps.Equal(binding.Properties, want[i]) {
			t.Errorf("Read's %s binding = %v, want %v", binding.Name, binding.Properties, want[i])
		}
	}
}

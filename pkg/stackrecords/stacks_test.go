package stackrecords_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

type listedEntries struct {
	keyvalue.Store
	mu     sync.Mutex
	listed int
}

func (l *listedEntries) List(ctx context.Context, in keyvalue.Partition, under ...string) ([]keyvalue.Entry, error) {
	entries, err := l.Store.List(ctx, in, under...)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.listed += len(entries)
	return entries, err
}

func writeStack(t *testing.T, store keyvalue.Store, tier environment.Tier, name naming.StackName, recorded stackrecords.Stack) {
	t.Helper()
	if err := stackrecords.Write(context.Background(), store, tier, "shop", name, recorded); err != nil {
		t.Fatal(err)
	}
}

func TestTheImagesAStackRecordsAreTheImageItStartsAndThoseItsContainersRun(t *testing.T) {
	t.Parallel()

	recorded := stackrecords.Stack{
		Image:      "r/shop.web:new",
		Containers: []provider.AppContainer{{Name: "web", Image: "r/shop.web:new"}, {Name: "web-worker", Image: "r/shop.web:worker"}},
	}

	want := []string{"r/shop.web:new", "r/shop.web:worker"}
	if got := recorded.Images(); !slices.Equal(got, want) {
		t.Errorf("Images() = %v, want %v", got, want)
	}
}

func TestTheImagesOfAnAppAreReadFromItsOwnStacksInEveryTier(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewKeyValues()
	writeStack(t, store, environment.TierPreview, naming.AppStack("pr-7", "web", naming.NewReleaseToken("b1", "")),
		stackrecords.Stack{Kind: provider.StackApp, App: "web", Image: "r/shop.web:b1"})
	writeStack(t, store, environment.TierProduction, naming.AppStack("prod", "web", naming.NewReleaseToken("b0", "")),
		stackrecords.Stack{Kind: provider.StackApp, App: "web", Image: "r/shop.web:b0"})
	writeStack(t, store, environment.TierPreview, naming.AppStack("pr-7", "api", naming.NewReleaseToken("b1", "")),
		stackrecords.Stack{Kind: provider.StackApp, App: "api", Image: "r/shop.api:b1"})

	got, err := stackrecords.ListRecordedAppImages(ctx, store, "shop", "web")
	if err != nil {
		t.Fatalf("ListRecordedAppImages() = %v", err)
	}

	want := map[string]bool{"r/shop.web:b1": true, "r/shop.web:b0": true}
	if !maps.Equal(got, want) {
		t.Errorf("ListRecordedAppImages() = %v, want %v", got, want)
	}
}

func TestTheImagesOfAnAppLeaveOutTheStackTheyAreReadFor(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewKeyValues()
	own := naming.AppStack("pr-7", "web", naming.NewReleaseToken("b1", ""))
	writeStack(t, store, environment.TierPreview, own, stackrecords.Stack{Kind: provider.StackApp, App: "web", Image: "r/shop.web:b1"})
	writeStack(t, store, environment.TierProduction, own, stackrecords.Stack{Kind: provider.StackApp, App: "web", Image: "r/shop.web:b0"})

	got, err := stackrecords.ListRecordedAppImages(ctx, store, "shop", "web", provider.StackRef{Project: "shop", Tier: environment.TierPreview, Name: own})
	if err != nil {
		t.Fatalf("ListRecordedAppImages() = %v", err)
	}

	if want := map[string]bool{"r/shop.web:b0": true}; !maps.Equal(got, want) {
		t.Errorf("ListRecordedAppImages() = %v, want %v: the same stack name in another tier is another stack", got, want)
	}
}

func TestReadingAnAppsImagesReadsNoEntryOfAnotherApp(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := &listedEntries{Store: fake.NewKeyValues()}
	for i := range 40 {
		app := fmt.Sprintf("app%d", i)
		writeStack(t, store, environment.TierPreview, naming.AppStack("pr-7", app, naming.NewReleaseToken("b1", "")),
			stackrecords.Stack{Kind: provider.StackApp, App: app, Image: "r/shop." + app + ":b1"})
	}
	writeStack(t, store, environment.TierPreview, naming.AppStack("pr-7", "web", naming.NewReleaseToken("b1", "")),
		stackrecords.Stack{Kind: provider.StackApp, App: "web", Image: "r/shop.web:b1"})
	store.listed = 0

	if _, err := stackrecords.ListRecordedAppImages(ctx, store, "shop", "web"); err != nil {
		t.Fatalf("ListRecordedAppImages() = %v", err)
	}

	if store.listed != 1 {
		t.Errorf("ListRecordedAppImages() read %d entries, want 1: the read grows with the app's own stacks, never with the project's", store.listed)
	}
}

func TestAForgottenStackNamesNoImage(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewKeyValues()
	web := naming.AppStack("prod", "web", naming.NewReleaseToken("b1", ""))
	writeStack(t, store, environment.TierProduction, web, stackrecords.Stack{Kind: provider.StackApp, App: "web", Image: "r/shop.web:b1"})

	if err := stackrecords.Forget(ctx, store, environment.TierProduction, "shop", web); err != nil {
		t.Fatal(err)
	}

	got, err := stackrecords.ListRecordedAppImages(ctx, store, "shop", "web")
	if err != nil || len(got) != 0 {
		t.Errorf("ListRecordedAppImages() = %v, %v, want none once the stack is forgotten", got, err)
	}
	if left, _ := store.List(ctx, stackrecords.StacksPartition(environment.TierProduction, "shop")); len(left) != 0 {
		t.Errorf("forgetting the stack left %d entries behind", len(left))
	}
}

func TestTheImageListOfAnAppLeavesTheStacksOfTheProjectAsTheyWere(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewKeyValues()
	web := naming.AppStack("prod", "web", naming.NewReleaseToken("b1", ""))
	writeStack(t, store, environment.TierProduction, web, stackrecords.Stack{Kind: provider.StackApp, App: "web", Image: "r/shop.web:b1"})

	stacks, err := stackrecords.List(ctx, store, environment.TierProduction, "shop")
	if err != nil {
		t.Fatal(err)
	}
	if len(stacks) != 1 || stacks[0].Name != web {
		t.Errorf("List() = %v, want only %s", stacks, web)
	}
}

type refusingRemoval struct {
	keyvalue.Store
	refused func(keyvalue.Key) bool
}

func (r refusingRemoval) Remove(ctx context.Context, key keyvalue.Key, expected keyvalue.Revision) error {
	if r.refused(key) {
		return errors.New("the store refused the removal")
	}
	return r.Store.Remove(ctx, key, expected)
}

func TestAForgetThatFailsLeavesTheRecordARetryFinds(t *testing.T) {
	t.Parallel()
	web := naming.AppStack("prod", "web", naming.NewReleaseToken("b1", ""))
	for name, refused := range map[string]func(keyvalue.Key) bool{
		"the record": func(key keyvalue.Key) bool {
			return key.String() == stackrecords.StackKey(environment.TierProduction, "shop", web).String()
		},
		"the images entry": func(key keyvalue.Key) bool {
			return key.String() != stackrecords.StackKey(environment.TierProduction, "shop", web).String()
		},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			base := fake.NewKeyValues()
			writeStack(t, base, environment.TierProduction, web, stackrecords.Stack{Kind: provider.StackApp, App: "web", Image: "r/shop.web:b1"})
			store := refusingRemoval{Store: base, refused: refused}

			if err := stackrecords.Forget(ctx, store, environment.TierProduction, "shop", web); err == nil {
				t.Fatal("Forget() = nil though the store refused a removal")
			}

			if _, found, err := stackrecords.Read(ctx, base, environment.TierProduction, "shop", web); err != nil || !found {
				t.Errorf("Read() = %t, %v after a failed Forget, want the record still there: an images entry without its record keeps its images forever, as no retry ever finds the stack again", found, err)
			}
		})
	}
}

func decodedRecord(t *testing.T, raw []byte) stackrecords.Stack {
	t.Helper()
	var stored stackrecords.Stack
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatalf("decoding the stored stack record = %v", err)
	}
	return stored
}

func TestAStackRecordHoldsNoSecretOfTheBindingsItNames(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewKeyValues()
	name := naming.InfraStack("main")
	secretKeys := []string{provider.PropertyPassword, provider.PropertyPassword, provider.PropertySigningKey, "secretAccessKey", "token"}

	err := stackrecords.Write(ctx, store, environment.TierProduction, "shop", name, stackrecords.Stack{
		Kind: provider.StackInfra,
		Bindings: []provider.Binding{
			{Type: provider.BindingPostgres, Name: "db", Properties: map[string]string{
				provider.PropertyHost: "db.internal", provider.PropertyPort: "5432",
				provider.PropertyDatabase: "app", provider.PropertyUsername: "admin",
				provider.PropertyPassword: "pg-password-s3cret",
			}},
			{Type: provider.BindingKV, Name: "cache", Properties: map[string]string{
				provider.PropertyHost: "cache.internal", provider.PropertyPassword: "kv-password-s3cret",
			}},
			{Type: provider.BindingRealtime, Name: "live", Properties: map[string]string{
				provider.PropertyURL: "/socket", provider.PropertySigningKey: "signing-key-s3cret",
				provider.PropertyVerifyKey: "verify",
			}},
			{Type: provider.BindingBucket, Name: "uploads", Properties: map[string]string{
				provider.PropertyBucket: "uploads", provider.PropertyEndpoint: "https://s3.internal",
				"accessKeyId": "AKIA", "secretAccessKey": "bucket-secret-access-key-s3cret",
			}},
			{Type: provider.BindingCustom, Name: "stripe", Properties: map[string]string{"token": "custom-token-s3cret"}},
		},
	})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	raw, err := keyvalue.ReadOrEmpty(ctx, store, stackrecords.StackKey(environment.TierProduction, "shop", name))
	if err != nil {
		t.Fatalf("read the raw record: %v", err)
	}
	stored := decodedRecord(t, raw.Value)
	if len(stored.Bindings) != len(secretKeys) {
		t.Fatalf("the stored stack record holds %d bindings, want the %d written", len(stored.Bindings), len(secretKeys))
	}
	for i, binding := range stored.Bindings {
		if _, held := binding.Properties[secretKeys[i]]; held {
			t.Errorf("the stored stack record holds binding %s's %q, and the record store is readable without the sealer", binding.Name, secretKeys[i])
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
	stored := decodedRecord(t, raw.Value)
	if len(stored.Bindings) != 3 {
		t.Fatalf("the stored stack record holds %d bindings, want the 3 written", len(stored.Bindings))
	}
	for i, unclassified := range []string{"adminToken", "topic"} {
		if _, held := stored.Bindings[i].Properties[unclassified]; held {
			t.Errorf("the stored stack record holds binding %s's %q, a property neither bindings.proto nor the record declares, so it may be a secret", stored.Bindings[i].Name, unclassified)
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

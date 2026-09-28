package ports_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"maps"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/envvars"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider/conformance"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/seal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
)

const (
	keyARN        = "arn:aws:kms:us-east-1:123456789012:key/ocel-vars"
	previewKeyARN = "arn:aws:kms:us-east-1:123456789012:key/ocel-vars-preview"
)

func newRecords(t *testing.T) (awsports.Records, *fakeDynamo) {
	t.Helper()
	ddb := newFakeDynamo()
	return awsports.Records{Dynamo: ddb, Tables: awsports.Table("ocel-state")}, ddb
}

type tierKeys map[environment.Tier]string

func (k tierKeys) Key(_ context.Context, tier environment.Tier) (string, error) { return k[tier], nil }

func newCipher() (awsports.Cipher, *fakeKMS) {
	crypto := &fakeKMS{}
	return awsports.Cipher{KMS: crypto, Keys: tierKeys{
		environment.TierProduction: keyARN,
		environment.TierPreview:    previewKeyARN,
	}}, crypto
}

func TestRecordsConformance(t *testing.T) {
	table, _ := newRecords(t)
	conformance.RunStore(t, table)
}

func TestCipherConformance(t *testing.T) {
	cipher, _ := newCipher()
	conformance.RunCipher(t, cipher)
}

func TestValueRecordsPartitionOnTheProjectAndTier(t *testing.T) {
	table, ddb := newRecords(t)
	scope := envvars.Scope{Project: "shop", Tier: environment.TierProduction}
	store := envvars.Store{Records: table, Cipher: newCipherIgnoringKMSCalls()}

	if _, err := store.Set(context.Background(), scope, envvars.Coordinate{Cell: envvars.Cell{Key: "STRIPE_API_KEY"}}, "sk_live_secret", nil); err != nil {
		t.Fatalf("Set err = %v", err)
	}

	partition, err := awsports.Partition(envvars.ScopedRecordName(scope))
	if err != nil {
		t.Fatalf("Partition err = %v", err)
	}
	for _, written := range ddb.partitions() {
		if written != partition {
			t.Errorf("a value write landed in partition %q, and a function's role is scoped to %q alone", written, partition)
		}
	}
	if !strings.Contains(partition, scope.Project) || !strings.Contains(partition, string(scope.Tier)) {
		t.Errorf("value partition = %q, want it to name both the project and the tier a role is granted", partition)
	}
}

func TestARecordNameShorterThanItsPartitionIsRefused(t *testing.T) {
	store, _ := newRecords(t)

	if _, err := store.List(context.Background(), records.Name{"values", "shop"}); err == nil {
		t.Fatal("List() under half a value partition succeeded, and answering it would have to walk the whole table")
	}
}

func TestASealedValueIsOpaqueAtRest(t *testing.T) {
	table, _ := newRecords(t)
	cipher, _ := newCipher()
	scope := envvars.Scope{Project: "shop", Tier: environment.TierProduction}
	store := envvars.Store{Records: table, Cipher: cipher}

	if _, err := store.Set(context.Background(), scope, envvars.Coordinate{Cell: envvars.Cell{Key: "STRIPE_API_KEY"}}, "sk_live_secret", nil); err != nil {
		t.Fatalf("Set err = %v", err)
	}

	stored, err := table.List(context.Background(), envvars.ScopedRecordName(scope))
	if err != nil {
		t.Fatalf("List err = %v", err)
	}
	for _, record := range stored {
		if bytes.Contains(record.Bytes, []byte("sk_live_secret")) {
			t.Fatalf("%s stores the plaintext at rest", record.Name)
		}
	}
}

func TestACellIsSealedUnderAnEncryptionContextNamingItsProjectClassEnvironmentFolderAndKey(t *testing.T) {
	table, _ := newRecords(t)
	cipher, crypto := newCipher()
	store := envvars.Store{Records: table, Cipher: cipher}
	scope := envvars.Scope{Project: "shop", Tier: environment.TierProduction}
	at := envvars.Coordinate{Cell: envvars.Cell{Folder: "/web", Key: "STRIPE_API_KEY"}, Environment: "staging"}

	if _, err := store.Set(context.Background(), scope, at, "sk_live_secret", nil); err != nil {
		t.Fatalf("Set err = %v", err)
	}

	want := map[string]string{
		"project":     "shop",
		"class":       "production",
		"environment": "staging",
		"folder":      "/web",
		"key":         "STRIPE_API_KEY",
	}
	if len(crypto.contexts) != 1 || !maps.Equal(crypto.contexts[0], want) {
		t.Fatalf("encryption context = %v, want exactly one %v: every value already stored is bound to it", crypto.contexts, want)
	}
	if len(crypto.keyIDs) != 1 || crypto.keyIDs[0] != keyARN {
		t.Errorf("sealed under %v, want %q", crypto.keyIDs, keyARN)
	}
}

func TestACellWhoseEncryptionContextNamesItsTierClassOpens(t *testing.T) {
	table, _ := newRecords(t)
	cipher, _ := newCipher()
	sealed := fakeCipherMarker + keyARN + "#class=production,environment=staging,folder=/web,key=STRIPE_API_KEY,project=shop|" +
		base64.StdEncoding.EncodeToString([]byte("sk_live_secret"))
	body, err := json.Marshal(map[string]any{"version": 1, "size": 14, "sealed": []byte(sealed)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := table.Write(context.Background(), records.Record{
		Name:  records.Name{"values", "shop", "production", "cells", "%2Fweb", "STRIPE_API_KEY", "staging"},
		Bytes: body,
	}); err != nil {
		t.Fatal(err)
	}
	store := envvars.Store{Records: table, Cipher: cipher}
	at := envvars.Coordinate{Cell: envvars.Cell{Folder: "/web", Key: "STRIPE_API_KEY"}, Environment: "staging"}

	value, err := store.Get(context.Background(), envvars.Scope{Project: "shop", Tier: environment.TierProduction}, at, true)
	if err != nil || value.Plaintext != "sk_live_secret" {
		t.Fatalf("Get() = %q, %v, want the value KMS sealed under class=production to open: the encryption context is data every stored value is bound to", value.Plaintext, err)
	}
}

func TestABindingIsSealedUnderAnEncryptionContextThatNamesIt(t *testing.T) {
	table, _ := newRecords(t)
	cipher, crypto := newCipher()
	store := envvars.Store{Records: table, Cipher: cipher}
	scope := envvars.Scope{Project: "shop", Tier: environment.TierPreview}

	if _, err := store.SetBinding(context.Background(), scope, "", envvars.OwnerOcel, "orders", envvars.BindingWrite{Record: []byte("{}"), Value: []byte("{}")}); err != nil {
		t.Fatalf("SetBinding err = %v", err)
	}

	want := map[string]string{
		"project":     "shop",
		"class":       "preview",
		"environment": "*",
		"folder":      "/",
		"binding":     "orders",
		"key":         "PROPERTIES",
	}
	if len(crypto.contexts) != 1 || !maps.Equal(crypto.contexts[0], want) {
		t.Fatalf("encryption context = %v, want exactly one %v: every binding already stored is bound to it", crypto.contexts, want)
	}
}

func TestAssociatedDataThatNamesOneFieldTwiceIsRefused(t *testing.T) {
	cipher, crypto := newCipher()
	bound := seal.AssociatedData{{Name: "key", Value: "A"}, {Name: "key", Value: "B"}}

	if _, err := cipher.Seal(context.Background(), environment.TierProduction, bound, []byte("v")); err == nil {
		t.Fatal("Seal() with key named twice succeeded, and an encryption context keeps one of the two values")
	}
	if len(crypto.contexts) != 0 {
		t.Errorf("KMS was asked to encrypt under %v, want no call", crypto.contexts)
	}
}

func TestAValueNamingNoTierIsRefusedRatherThanSealedUnderSomeKey(t *testing.T) {
	cipher, crypto := newCipher()

	_, err := cipher.Seal(context.Background(), "", seal.AssociatedData{{Name: "key", Value: "K"}}, []byte("v"))
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
		t.Fatalf("Seal() under no tier = %v, want an invalid refusal: each tier is sealed under the key its own bootstrap made", err)
	}
	if len(crypto.keyIDs) != 0 {
		t.Errorf("KMS was asked to encrypt under %v, want no call", crypto.keyIDs)
	}
}

func newCipherIgnoringKMSCalls() seal.Cipher {
	cipher, _ := newCipher()
	return cipher
}

func TestAnAccountWithNoBootstrapHasNoRecords(t *testing.T) {
	t.Parallel()

	store := awsports.Records{Dynamo: newFakeDynamo()}
	name := records.Name{"bootstrap", "production"}

	if _, err := store.Read(context.Background(), name); !errors.Is(err, records.ErrNotFound) {
		t.Errorf("Read() with no bootstrap installed = %v, want ErrNotFound", err)
	}
	listed, err := store.List(context.Background(), records.Name{"projects", "production"})
	if err != nil || len(listed) != 0 {
		t.Errorf("List() with no bootstrap installed = %v, %v, want nothing", listed, err)
	}
	if err := store.Remove(context.Background(), name, "whatever"); !errors.Is(err, records.ErrNotFound) {
		t.Errorf("Remove() with no bootstrap installed = %v, want ErrNotFound", err)
	}

	_, err = store.Write(context.Background(), records.Record{Name: name, Bytes: []byte("{}")})
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady {
		t.Fatalf("Write() with no bootstrap installed = %v, want a %s refusal rather than a silent no-op", err, refusal.CodeNotReady)
	}
	if !strings.Contains(refused.Message, "ocel bootstrap") {
		t.Errorf("Write() refusal = %q, want it to name what creates the bootstrap", refused.Message)
	}
}

func TestATableDeletedMidTeardownHasNoRecords(t *testing.T) {
	t.Parallel()

	dynamo := newFakeDynamo()
	dynamo.gone = true
	store := awsports.Records{Dynamo: dynamo, Tables: awsports.Table("ocel-bootstrap-state")}
	name := records.Name{"bootstrap", "production"}

	if _, err := store.Read(context.Background(), name); !errors.Is(err, records.ErrNotFound) {
		t.Errorf("Read() against a deleted table = %v, want ErrNotFound", err)
	}
	listed, err := store.List(context.Background(), records.Name{"projects", "production"})
	if err != nil || len(listed) != 0 {
		t.Errorf("List() against a deleted table = %v, %v, want nothing", listed, err)
	}
	if err := store.Remove(context.Background(), name, "whatever"); !errors.Is(err, records.ErrNotFound) {
		t.Errorf("Remove() against a deleted table = %v, want ErrNotFound", err)
	}
	if _, err := store.Write(context.Background(), records.Record{Name: name, Bytes: []byte("{}")}); err == nil {
		t.Error("Write() against a deleted table = nil, want the failure surfaced")
	}
}

func TestNoRootKeepsAWholeAccountInOnePartition(t *testing.T) {
	for _, name := range []records.Name{
		stackrecords.ProjectsRecord(environment.TierProduction),
		stackrecords.BootstrapRecord(environment.TierProduction),
		stackrecords.WildcardRecord(environment.TierPreview),
		stackrecords.EdgeStacksRecord(environment.TierPreview),
		stackrecords.StacksRecord(environment.TierProduction, "shop"),
		stackrecords.EnvironmentsRecord(environment.TierPreview, "shop"),
	} {
		partition, err := awsports.Partition(name)
		if err != nil {
			t.Fatalf("Partition(%s) err = %v", name, err)
		}
		if partition == name[0] {
			t.Errorf("%s partitions on %q alone, so every account's %s records share one key", name, partition, name[0])
		}
	}
}

func TestTheSchemaRecordSitsOnTheSameKeyEveryLayoutWrote(t *testing.T) {
	for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
		partition, err := awsports.Partition(stackrecords.SchemaRecord(tier))
		if err != nil {
			t.Fatalf("Partition(%s) err = %v", stackrecords.SchemaRecord(tier), err)
		}
		if partition != records.RootSchema {
			t.Errorf("the %s schema record partitions on %q, want %q: a build that cannot find the schema an older layout wrote reads it as unwritten and stamps its own over live records",
				tier, partition, records.RootSchema)
		}
	}
}

func TestOneProjectsStacksDoNotShareAPartitionWithAnothers(t *testing.T) {
	shop, err := awsports.Partition(stackrecords.StackRecord(environment.TierProduction, "shop", naming.InfraStack("shop")))
	if err != nil {
		t.Fatalf("Partition err = %v", err)
	}
	web, err := awsports.Partition(stackrecords.StackRecord(environment.TierProduction, "web", naming.InfraStack("web")))
	if err != nil {
		t.Fatalf("Partition err = %v", err)
	}
	if shop == web {
		t.Errorf("both projects' stacks land in %q, and one project's deploys then throttle the other's", shop)
	}
	if !strings.Contains(shop, "shop") {
		t.Errorf("stack partition = %q, want it to name the project whose deploys it contains", shop)
	}
}

func TestABindingsPairSharesOnePrefixInsideTheProjectPartition(t *testing.T) {
	table, ddb := newRecords(t)
	scope := envvars.Scope{Project: "shop", Tier: environment.TierProduction}
	store := envvars.Store{Records: table, Cipher: newCipherIgnoringKMSCalls()}

	if _, err := store.SetBinding(context.Background(), scope, "", envvars.OwnerOcel, "db",
		envvars.BindingWrite{Record: []byte("{}"), Value: []byte("{}")}); err != nil {
		t.Fatalf("SetBinding err = %v", err)
	}

	partition, err := awsports.Partition(envvars.ScopedRecordName(scope))
	if err != nil {
		t.Fatalf("Partition err = %v", err)
	}
	for _, written := range ddb.partitions() {
		if written != partition {
			t.Errorf("a binding write landed in partition %q, and a function's role is scoped to %q alone", written, partition)
		}
	}

	prefix := "bindings#db#"
	under := 0
	for _, sk := range ddb.sortKeys(partition) {
		if !strings.HasPrefix(sk, "bindings#") {
			continue
		}
		if !strings.HasPrefix(sk, prefix) {
			t.Errorf("a binding record sorts at %q, want the whole pair under %q so one query returns it", sk, prefix)
			continue
		}
		under++
	}
	if under != 2 {
		t.Errorf("%d records sort under %q, want the record and the value beside it", under, prefix)
	}
}

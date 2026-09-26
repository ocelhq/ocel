package ports_test

import (
	"bytes"
	"context"
	"errors"
	"maps"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit/conformance"
	"github.com/ocelhq/ocel/pkg/providerkit/envvars"
	"github.com/ocelhq/ocel/pkg/providerkit/records"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	"github.com/ocelhq/ocel/pkg/providerkit/stackrecords"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const keyARN = "arn:aws:kms:us-east-1:123456789012:key/ocel-vars"

func newRecords(t *testing.T) (awsports.Records, *fakeDynamo) {
	t.Helper()
	ddb := newFakeDynamo()
	return awsports.Records{Dynamo: ddb, Tables: awsports.Table("ocel-state")}, ddb
}

func newSealer() (awsports.Cipher, *fakeKMS) {
	crypto := &fakeKMS{}
	return awsports.Cipher{KMS: crypto, Keys: awsports.Key(keyARN)}, crypto
}

func TestRecordsConformance(t *testing.T) {
	records, _ := newRecords(t)
	conformance.RunRecordStore(t, records)
}

func TestSealerConformance(t *testing.T) {
	sealer, _ := newSealer()
	conformance.RunCipher(t, sealer)
}

func TestValueRecordsPartitionOnTheProjectAndClass(t *testing.T) {
	records, ddb := newRecords(t)
	scope := envvars.Scope{Project: "shop", Class: edge.ClassProduction}
	store := envvars.Store{Records: records, Cipher: mustSealer()}

	if _, err := store.Set(context.Background(), scope, envvars.Coordinate{Cell: envvars.Cell{Key: "STRIPE_API_KEY"}}, "sk_live_secret", nil); err != nil {
		t.Fatalf("Set err = %v", err)
	}

	partition, err := awsports.Partition(envvars.ScopedRecordName(scope))
	if err != nil {
		t.Fatalf("Partition err = %v", err)
	}
	for _, held := range ddb.partitions() {
		if held != partition {
			t.Errorf("a value write landed in partition %q, and a function's role is scoped to %q alone", held, partition)
		}
	}
	if !strings.Contains(partition, scope.Project) || !strings.Contains(partition, string(scope.Class)) {
		t.Errorf("value partition = %q, want it to name both the project and the class a role is granted", partition)
	}
}

func TestARecordNameShorterThanItsPartitionIsRefused(t *testing.T) {
	store, _ := newRecords(t)

	if _, err := store.List(context.Background(), records.Name{"values", "shop"}); err == nil {
		t.Fatal("List() under half a value partition succeeded, and answering it would have to walk the whole table")
	}
}

func TestASealedValueIsOpaqueAtRest(t *testing.T) {
	records, _ := newRecords(t)
	sealer, _ := newSealer()
	scope := envvars.Scope{Project: "shop", Class: edge.ClassProduction}
	store := envvars.Store{Records: records, Cipher: sealer}

	if _, err := store.Set(context.Background(), scope, envvars.Coordinate{Cell: envvars.Cell{Key: "STRIPE_API_KEY"}}, "sk_live_secret", nil); err != nil {
		t.Fatalf("Set err = %v", err)
	}

	held, err := records.List(context.Background(), envvars.ScopedRecordName(scope))
	if err != nil {
		t.Fatalf("List err = %v", err)
	}
	for _, record := range held {
		if bytes.Contains(record.Bytes, []byte("sk_live_secret")) {
			t.Fatalf("%s holds the plaintext at rest", record.Name)
		}
	}
}

func TestTheEncryptionContextNamesEveryComponentOfTheCoordinate(t *testing.T) {
	sealer, crypto := newSealer()

	at := records.SealScope{
		Project: "shop",
		Class:   edge.ClassProduction,
		Env:     "staging",
		Folder:  "/web",
		Name:    "STRIPE_API_KEY",
	}
	if _, err := sealer.Seal(context.Background(), at, []byte("sk_live_secret")); err != nil {
		t.Fatalf("Seal err = %v", err)
	}

	want := map[string]string{
		"project":     "shop",
		"class":       string(edge.ClassProduction),
		"environment": "staging",
		"folder":      "/web",
		"key":         "STRIPE_API_KEY",
	}
	if len(crypto.contexts) != 1 || !maps.Equal(crypto.contexts[0], want) {
		t.Fatalf("encryption context = %v, want exactly one %v", crypto.contexts, want)
	}
	if len(crypto.keyIDs) != 1 || crypto.keyIDs[0] != keyARN {
		t.Errorf("sealed under %v, want %q", crypto.keyIDs, keyARN)
	}
}

func TestABindingSealsUnderItsOwnName(t *testing.T) {
	sealer, crypto := newSealer()

	at := records.SealScope{
		Project: "shop",
		Class:   edge.ClassPreview,
		Env:     "*",
		Folder:  "/",
		Binding: "orders",
		Name:    "PROPERTIES",
	}
	if _, err := sealer.Seal(context.Background(), at, []byte("{}")); err != nil {
		t.Fatalf("Seal err = %v", err)
	}
	if crypto.contexts[0]["binding"] != "orders" {
		t.Errorf("encryption context = %v, want it to name the binding the value belongs to", crypto.contexts[0])
	}
}

func TestACoordinateMissingAComponentIsRefused(t *testing.T) {
	sealer, _ := newSealer()

	for name, at := range map[string]records.SealScope{
		"no project":     {Class: edge.ClassProduction, Env: "*", Folder: "/", Name: "K"},
		"no class":       {Project: "shop", Env: "*", Folder: "/", Name: "K"},
		"no environment": {Project: "shop", Class: edge.ClassProduction, Folder: "/", Name: "K"},
		"no folder":      {Project: "shop", Class: edge.ClassProduction, Env: "*", Name: "K"},
		"no key":         {Project: "shop", Class: edge.ClassProduction, Env: "*", Folder: "/"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := sealer.Seal(context.Background(), at, []byte("v")); err == nil {
				t.Fatal("Seal() at a coordinate missing a component succeeded, so two cells could seal alike")
			}
		})
	}
}

func mustSealer() records.Cipher {
	sealer, _ := newSealer()
	return sealer
}

func TestAnAccountWithNoBootstrapHoldsNoRecords(t *testing.T) {
	t.Parallel()

	store := awsports.Records{Dynamo: newFakeDynamo()}
	name := records.Name{"bootstrap", "production"}

	if _, err := store.Read(context.Background(), name); !errors.Is(err, records.ErrNotFound) {
		t.Errorf("Read() with no bootstrap standing = %v, want ErrNotFound", err)
	}
	held, err := store.List(context.Background(), records.Name{"projects", "production"})
	if err != nil || len(held) != 0 {
		t.Errorf("List() with no bootstrap standing = %v, %v, want nothing", held, err)
	}
	if err := store.Remove(context.Background(), name, "whatever"); !errors.Is(err, records.ErrNotFound) {
		t.Errorf("Remove() with no bootstrap standing = %v, want ErrNotFound", err)
	}

	_, err = store.Write(context.Background(), records.Record{Name: name, Bytes: []byte("{}")})
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady {
		t.Fatalf("Write() with no bootstrap standing = %v, want a %s refusal rather than a silent no-op", err, refusal.CodeNotReady)
	}
	if !strings.Contains(refused.Message, "ocel bootstrap") {
		t.Errorf("Write() refusal = %q, want it to name what creates the bootstrap", refused.Message)
	}
}

func TestATableDeletedMidTeardownHoldsNoRecords(t *testing.T) {
	t.Parallel()

	dynamo := newFakeDynamo()
	dynamo.gone = true
	store := awsports.Records{Dynamo: dynamo, Tables: awsports.Table("ocel-bootstrap-state")}
	name := records.Name{"bootstrap", "production"}

	if _, err := store.Read(context.Background(), name); !errors.Is(err, records.ErrNotFound) {
		t.Errorf("Read() against a deleted table = %v, want ErrNotFound", err)
	}
	held, err := store.List(context.Background(), records.Name{"projects", "production"})
	if err != nil || len(held) != 0 {
		t.Errorf("List() against a deleted table = %v, %v, want nothing", held, err)
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
		stackrecords.ProjectsRecord(edge.ClassProduction),
		stackrecords.BootstrapRecord(edge.ClassProduction),
		stackrecords.WildcardRecord(edge.ClassPreview),
		stackrecords.EdgeStacksRecord(edge.ClassPreview),
		stackrecords.StacksRecord(edge.ClassProduction, "shop"),
		stackrecords.EnvironmentsRecord(edge.ClassPreview, "shop"),
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
	for _, class := range []edge.Class{edge.ClassProduction, edge.ClassPreview} {
		partition, err := awsports.Partition(stackrecords.SchemaRecord(class))
		if err != nil {
			t.Fatalf("Partition(%s) err = %v", stackrecords.SchemaRecord(class), err)
		}
		if partition != records.RootSchema {
			t.Errorf("the %s schema record partitions on %q, want %q: a build that cannot find the schema an older layout wrote reads it as unwritten and stamps its own over live records",
				class, partition, records.RootSchema)
		}
	}
}

func TestOneProjectsStacksDoNotShareAPartitionWithAnothers(t *testing.T) {
	shop, err := awsports.Partition(stackrecords.StackRecord(edge.ClassProduction, "shop", naming.InfraStack("shop")))
	if err != nil {
		t.Fatalf("Partition err = %v", err)
	}
	web, err := awsports.Partition(stackrecords.StackRecord(edge.ClassProduction, "web", naming.InfraStack("web")))
	if err != nil {
		t.Fatalf("Partition err = %v", err)
	}
	if shop == web {
		t.Errorf("both projects' stacks land in %q, and one project's deploys then throttle the other's", shop)
	}
	if !strings.Contains(shop, "shop") {
		t.Errorf("stack partition = %q, want it to name the project whose deploys it holds", shop)
	}
}

func TestABindingsPairSharesOnePrefixInsideTheProjectPartition(t *testing.T) {
	records, ddb := newRecords(t)
	scope := envvars.Scope{Project: "shop", Class: edge.ClassProduction}
	store := envvars.Store{Records: records, Cipher: mustSealer()}

	if _, err := store.SetBinding(context.Background(), scope, "", envvars.OwnerOcel, "db",
		envvars.BindingWrite{Record: []byte("{}"), Value: []byte("{}")}); err != nil {
		t.Fatalf("SetBinding err = %v", err)
	}

	partition, err := awsports.Partition(envvars.ScopedRecordName(scope))
	if err != nil {
		t.Fatalf("Partition err = %v", err)
	}
	for _, held := range ddb.partitions() {
		if held != partition {
			t.Errorf("a binding write landed in partition %q, and a function's role is scoped to %q alone", held, partition)
		}
	}

	prefix := "bindings#db#"
	under := 0
	for _, sk := range ddb.sortKeys(partition) {
		if !strings.HasPrefix(sk, "bindings#") {
			continue
		}
		if !strings.HasPrefix(sk, prefix) {
			t.Errorf("a binding record sorts at %q, want the whole pair under %q so one query holds it", sk, prefix)
			continue
		}
		under++
	}
	if under != 2 {
		t.Errorf("%d records sort under %q, want the record and the value beside it", under, prefix)
	}
}

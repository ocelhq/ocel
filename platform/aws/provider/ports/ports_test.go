package ports_test

import (
	"bytes"
	"context"
	"errors"
	"maps"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/conformance"
	kit "github.com/ocelhq/ocel/pkg/providerkit/ports"
	"github.com/ocelhq/ocel/pkg/providerkit/values"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const keyARN = "arn:aws:kms:us-east-1:123456789012:key/ocel-vars"

func newRecords(t *testing.T) (awsports.Records, *fakeDynamo) {
	t.Helper()
	ddb := newFakeDynamo()
	return awsports.Records{Dynamo: ddb, Tables: awsports.Table("ocel-state")}, ddb
}

func newSealer() (awsports.Sealer, *fakeKMS) {
	crypto := &fakeKMS{}
	return awsports.Sealer{KMS: crypto, Keys: awsports.Key(keyARN)}, crypto
}

func TestRecordsConformance(t *testing.T) {
	records, _ := newRecords(t)
	conformance.RunRecordStore(t, records)
}

func TestSealerConformance(t *testing.T) {
	sealer, _ := newSealer()
	conformance.RunSealer(t, sealer)
}

func TestValueRecordsPartitionOnTheProjectAndClass(t *testing.T) {
	records, ddb := newRecords(t)
	scope := values.Scope{Project: "shop", Class: edge.ClassProduction}
	store := values.Store{Records: records, Sealer: mustSealer()}

	if _, err := store.Set(context.Background(), scope, values.Coordinate{Cell: values.Cell{Key: "STRIPE_API_KEY"}}, "sk_live_secret", nil); err != nil {
		t.Fatalf("Set err = %v", err)
	}

	partition, err := awsports.Partition(values.Under(scope))
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
	records, _ := newRecords(t)

	if _, err := records.List(context.Background(), kit.RecordName{"values", "shop"}); err == nil {
		t.Fatal("List() under half a value partition succeeded, and answering it would have to walk the whole table")
	}
}

func TestASealedValueIsOpaqueAtRest(t *testing.T) {
	records, _ := newRecords(t)
	sealer, _ := newSealer()
	scope := values.Scope{Project: "shop", Class: edge.ClassProduction}
	store := values.Store{Records: records, Sealer: sealer}

	if _, err := store.Set(context.Background(), scope, values.Coordinate{Cell: values.Cell{Key: "STRIPE_API_KEY"}}, "sk_live_secret", nil); err != nil {
		t.Fatalf("Set err = %v", err)
	}

	held, err := records.List(context.Background(), values.Under(scope))
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

	at := kit.Coordinate{
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

func TestALinkSealsUnderItsOwnName(t *testing.T) {
	sealer, crypto := newSealer()

	at := kit.Coordinate{
		Project: "shop",
		Class:   edge.ClassPreview,
		Env:     "*",
		Folder:  "/",
		Link:    "orders",
		Name:    "PROPERTIES",
	}
	if _, err := sealer.Seal(context.Background(), at, []byte("{}")); err != nil {
		t.Fatalf("Seal err = %v", err)
	}
	if crypto.contexts[0]["link"] != "orders" {
		t.Errorf("encryption context = %v, want it to name the link the value belongs to", crypto.contexts[0])
	}
}

func TestACoordinateMissingAComponentIsRefused(t *testing.T) {
	sealer, _ := newSealer()

	for name, at := range map[string]kit.Coordinate{
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

func mustSealer() kit.Sealer {
	sealer, _ := newSealer()
	return sealer
}

func TestAnAccountWithNoBootstrapHoldsNoRecords(t *testing.T) {
	t.Parallel()

	records := awsports.Records{Dynamo: newFakeDynamo()}
	name := kit.RecordName{"bootstrap", "production"}

	if _, err := records.Read(context.Background(), name); !errors.Is(err, kit.ErrNoRecord) {
		t.Errorf("Read() with no bootstrap standing = %v, want ErrNoRecord", err)
	}
	held, err := records.List(context.Background(), kit.RecordName{"projects", "production"})
	if err != nil || len(held) != 0 {
		t.Errorf("List() with no bootstrap standing = %v, %v, want nothing", held, err)
	}
	if err := records.Remove(context.Background(), name, "whatever"); !errors.Is(err, kit.ErrNoRecord) {
		t.Errorf("Remove() with no bootstrap standing = %v, want ErrNoRecord", err)
	}

	_, err = records.Write(context.Background(), kit.Record{Name: name, Bytes: []byte("{}")})
	var refusal kit.Refusal
	if !errors.As(err, &refusal) || refusal.Code != kit.CodeNotReady {
		t.Fatalf("Write() with no bootstrap standing = %v, want a %s refusal rather than a silent no-op", err, kit.CodeNotReady)
	}
	if !strings.Contains(refusal.Message, "ocel bootstrap") {
		t.Errorf("Write() refusal = %q, want it to name what creates the bootstrap", refusal.Message)
	}
}

func TestNoRootKeepsAWholeAccountInOnePartition(t *testing.T) {
	for _, name := range []kit.RecordName{
		providerkit.ProjectsRecord(providerkit.ClassProduction),
		providerkit.BootstrapRecord(providerkit.ClassProduction),
		providerkit.WildcardRecord(providerkit.ClassPreview),
		providerkit.EdgeStacksRecord(providerkit.ClassPreview),
		providerkit.StacksRecord(providerkit.ClassProduction, "shop"),
		providerkit.EnvironmentsRecord(providerkit.ClassPreview, "shop"),
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
	for _, class := range []providerkit.Class{providerkit.ClassProduction, providerkit.ClassPreview} {
		partition, err := awsports.Partition(providerkit.SchemaRecord(class))
		if err != nil {
			t.Fatalf("Partition(%s) err = %v", providerkit.SchemaRecord(class), err)
		}
		if partition != kit.RootSchema {
			t.Errorf("the %s schema record partitions on %q, want %q: a build that cannot find the schema an older layout wrote reads it as unwritten and stamps its own over live records",
				class, partition, kit.RootSchema)
		}
	}
}

func TestOneProjectsStacksDoNotShareAPartitionWithAnothers(t *testing.T) {
	shop, err := awsports.Partition(providerkit.StackRecord(providerkit.ClassProduction, "shop", naming.InfraStack("shop")))
	if err != nil {
		t.Fatalf("Partition err = %v", err)
	}
	web, err := awsports.Partition(providerkit.StackRecord(providerkit.ClassProduction, "web", naming.InfraStack("web")))
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

func TestALinksPairSharesOnePrefixInsideTheProjectPartition(t *testing.T) {
	records, ddb := newRecords(t)
	scope := values.Scope{Project: "shop", Class: edge.ClassProduction}
	store := values.Store{Records: records, Sealer: mustSealer()}

	if _, err := store.SetLink(context.Background(), scope, "", values.OwnerOcel, "db",
		values.Pair{Record: []byte("{}"), Value: []byte("{}")}); err != nil {
		t.Fatalf("SetLink err = %v", err)
	}

	partition, err := awsports.Partition(values.Under(scope))
	if err != nil {
		t.Fatalf("Partition err = %v", err)
	}
	for _, held := range ddb.partitions() {
		if held != partition {
			t.Errorf("a link write landed in partition %q, and a function's role is scoped to %q alone", held, partition)
		}
	}

	prefix := "links#db#"
	under := 0
	for _, sk := range ddb.sortKeys(partition) {
		if !strings.HasPrefix(sk, "links#") {
			continue
		}
		if !strings.HasPrefix(sk, prefix) {
			t.Errorf("a link record sorts at %q, want the whole pair under %q so one query holds it", sk, prefix)
			continue
		}
		under++
	}
	if under != 2 {
		t.Errorf("%d records sort under %q, want the record and the value beside it", under, prefix)
	}
}

package ports_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider/conformance"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/seal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/pkg/variablestore"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
)

const (
	keyARN        = "arn:aws:kms:us-east-1:123456789012:key/ocel-variables"
	previewKeyARN = "arn:aws:kms:us-east-1:123456789012:key/ocel-variables-preview"
)

type tierTables struct{}

func (tierTables) Table(_ context.Context, tier environment.Tier) (string, error) {
	return "ocel-" + string(tier) + "-state", nil
}

func (tierTables) ValuesTable(_ context.Context, tier environment.Tier) (string, error) {
	return "ocel-" + string(tier) + "-variables", nil
}

func newKeyValues(t *testing.T) (awsports.KeyValues, *fakeDynamo) {
	t.Helper()
	ddb := newFakeDynamo()
	return awsports.KeyValues{Dynamo: ddb, Tables: tierTables{}}, ddb
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

func TestTheKeyValueStoreConformsOverATableForEachTier(t *testing.T) {
	table, _ := newKeyValues(t)
	conformance.RunStore(t, table)
}

func TestTheCipherSealsAsEveryCipherMust(t *testing.T) {
	cipher, _ := newCipher()
	conformance.RunCipher(t, cipher)
}

func TestValueEntriesPartitionOnTheProjectAndTier(t *testing.T) {
	table, ddb := newKeyValues(t)
	scope := variablestore.Scope{Project: "shop", Tier: environment.TierProduction}
	store := variablestore.Store{KeyValues: table, Cipher: newCipherIgnoringKMSCalls()}

	if _, err := store.Set(context.Background(), scope, variablestore.Coordinate{Cell: variablestore.Cell{Key: "STRIPE_API_KEY"}}, "sk_live_secret", nil); err != nil {
		t.Fatalf("Set err = %v", err)
	}

	partition := awsports.PartitionKey(variablestore.ValuesPartition(scope))
	for _, written := range ddb.partitions() {
		if written != partition {
			t.Errorf("a value write landed in partition %q, and a function's role is scoped to %q alone", written, partition)
		}
	}
	if partition != "values#shop" {
		t.Errorf("value partition = %q, want it to name the project a role is granted", partition)
	}
	if got := ddb.tablesUsed(); !slices.Equal(got, []string{"ocel-production-variables"}) {
		t.Errorf("a production value reached tables %v, want the production values table alone: the table is the tier a role is granted", got)
	}
}

func TestAnEntryIsKeptAsTheJSONItHoldsInAStringAttribute(t *testing.T) {
	table, ddb := newKeyValues(t)
	key := stackrecords.ProjectKey(environment.TierProduction, "shop")

	if _, err := table.Write(context.Background(), records.Entry{Key: key, Value: []byte(`{"parts":["cache"]}`)}); err != nil {
		t.Fatal(err)
	}
	item := ddb.item("projects", "shop#")
	value, ok := item["value"].(*ddbtypes.AttributeValueMemberS)
	if !ok || value.Value != `{"parts":["cache"]}` {
		t.Fatalf("the item holds value %#v, want the JSON as a string a reader of the table sees as written", item["value"])
	}
}

func TestAnItemWithNoValueIsRefusedRatherThanReadAsEmpty(t *testing.T) {
	table, ddb := newKeyValues(t)
	key := stackrecords.ProjectKey(environment.TierProduction, "shop")
	ddb.items["ocel-production-state"] = map[string]map[string]map[string]ddbtypes.AttributeValue{"projects": {"shop#": {
		"pk":  &ddbtypes.AttributeValueMemberS{Value: "projects"},
		"sk":  &ddbtypes.AttributeValueMemberS{Value: "shop#"},
		"rev": &ddbtypes.AttributeValueMemberS{Value: "0123456789abcdef"},
	}}}

	var refused refusal.Refusal
	if entry, err := table.Read(context.Background(), key); !errors.As(err, &refused) || refused.Code != refusal.CodeDenied {
		t.Fatalf("Read(%s) of an item with no value = %+v, %v, want a %s refusal: an empty entry reads as one never written", key, entry, err, refusal.CodeDenied)
	}
}

func TestASealedValueIsOpaqueAtRest(t *testing.T) {
	table, _ := newKeyValues(t)
	cipher, _ := newCipher()
	scope := variablestore.Scope{Project: "shop", Tier: environment.TierProduction}
	store := variablestore.Store{KeyValues: table, Cipher: cipher}

	if _, err := store.Set(context.Background(), scope, variablestore.Coordinate{Cell: variablestore.Cell{Key: "STRIPE_API_KEY"}}, "sk_live_secret", nil); err != nil {
		t.Fatalf("Set err = %v", err)
	}

	stored, err := table.List(context.Background(), variablestore.ValuesPartition(scope))
	if err != nil {
		t.Fatalf("List err = %v", err)
	}
	for _, entry := range stored {
		if bytes.Contains(entry.Value, []byte("sk_live_secret")) {
			t.Fatalf("%s stores the plaintext at rest", entry.Key)
		}
	}
}

func TestACellIsSealedUnderAnEncryptionContextBindingItAsAVariableOfItsProjectTierEnvironmentFolderAndKey(t *testing.T) {
	table, _ := newKeyValues(t)
	cipher, crypto := newCipher()
	store := variablestore.Store{KeyValues: table, Cipher: cipher}
	scope := variablestore.Scope{Project: "shop", Tier: environment.TierProduction}
	at := variablestore.Coordinate{Cell: variablestore.Cell{Folder: "/web", Key: "STRIPE_API_KEY"}, Environment: "staging"}

	if _, err := store.Set(context.Background(), scope, at, "sk_live_secret", nil); err != nil {
		t.Fatalf("Set err = %v", err)
	}

	want := map[string]string{"ocel:associated-data": encodedContext("8:variable,4:shop,10:production,7:staging,4:/web,14:STRIPE_API_KEY,")}
	if len(crypto.contexts) != 1 || !maps.Equal(crypto.contexts[0], want) {
		t.Fatalf("encryption context = %v, want exactly one %v: every value already stored is bound to it", crypto.contexts, want)
	}
	if len(crypto.keyIDs) != 1 || crypto.keyIDs[0] != keyARN {
		t.Errorf("sealed under %v, want %q", crypto.keyIDs, keyARN)
	}
}

func TestACellWhoseEncryptionContextBindsItAsAVariableOpens(t *testing.T) {
	table, _ := newKeyValues(t)
	cipher, _ := newCipher()
	sealed := fakeCipherMarker + keyARN + "#ocel:associated-data=" + encodedContext("8:variable,4:shop,10:production,7:staging,4:/web,14:STRIPE_API_KEY,") + "|" +
		base64.StdEncoding.EncodeToString([]byte("sk_live_secret"))
	body, err := json.Marshal(map[string]any{"version": 1, "size": 14, "sealed": []byte(sealed)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := table.Write(context.Background(), records.Entry{
		Key:   variablestore.ValuesPartition(variablestore.Scope{Project: "shop", Tier: environment.TierProduction}).Key("cells", "/web", "STRIPE_API_KEY", "staging"),
		Value: body,
	}); err != nil {
		t.Fatal(err)
	}
	store := variablestore.Store{KeyValues: table, Cipher: cipher}
	at := variablestore.Coordinate{Cell: variablestore.Cell{Folder: "/web", Key: "STRIPE_API_KEY"}, Environment: "staging"}

	value, err := store.Get(context.Background(), variablestore.Scope{Project: "shop", Tier: environment.TierProduction}, at, true)
	if err != nil || value.Plaintext != "sk_live_secret" {
		t.Fatalf("Get() = %q, %v, want the value KMS sealed as a variable of shop to open: the encryption context is data every stored value is bound to", value.Plaintext, err)
	}
}

func TestABindingIsSealedUnderAnEncryptionContextBindingItAsABindingOfItsProjectTierEnvironmentAndName(t *testing.T) {
	table, _ := newKeyValues(t)
	cipher, crypto := newCipher()
	store := variablestore.Store{KeyValues: table, Cipher: cipher}
	scope := variablestore.Scope{Project: "shop", Tier: environment.TierPreview}

	if _, err := store.SetBinding(context.Background(), scope, "", variablestore.OwnerOcel, "orders", variablestore.BindingWrite{Record: []byte("{}"), Value: []byte("{}")}); err != nil {
		t.Fatalf("SetBinding err = %v", err)
	}

	want := map[string]string{"ocel:associated-data": encodedContext("7:binding,4:shop,7:preview,1:*,6:orders,")}
	if len(crypto.contexts) != 1 || !maps.Equal(crypto.contexts[0], want) {
		t.Fatalf("encryption context = %v, want exactly one %v: every binding already stored is bound to it", crypto.contexts, want)
	}
}

func TestABindingWhoseEncryptionContextBindsItAsABindingOpens(t *testing.T) {
	table, _ := newKeyValues(t)
	cipher, _ := newCipher()
	scope := variablestore.Scope{Project: "shop", Tier: environment.TierProduction}
	sealed := fakeCipherMarker + keyARN + "#ocel:associated-data=" + encodedContext("7:binding,4:shop,10:production,1:*,6:orders,") + "|" +
		base64.StdEncoding.EncodeToString([]byte(`{"url":"postgres://orders"}`))
	value, err := json.Marshal(map[string]any{"version": 1, "sealed": []byte(sealed)})
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string][]byte{
		"records": []byte(`{"version":1,"record":"eyJuYW1lIjoib3JkZXJzIn0=","owner":"ocel"}`),
		"values":  value,
	} {
		if _, err := table.Write(context.Background(), records.Entry{
			Key:   variablestore.ValuesPartition(scope).Key("bindings", "orders", name, "*"),
			Value: body,
		}); err != nil {
			t.Fatal(err)
		}
	}
	store := variablestore.Store{KeyValues: table, Cipher: cipher}

	resolved, err := store.ResolveBinding(context.Background(), scope, "", "orders")
	if err != nil || string(resolved.Value) != `{"url":"postgres://orders"}` {
		t.Fatalf("ResolveBinding() = %q, %v, want the value KMS sealed as the binding orders to open: the encryption context is data every stored binding is bound to", resolved.Value, err)
	}
}

func TestAnEncryptionContextIsValidUTF8ThatDecodesBackToTheAssociatedDataEvenWhenAValueIsNot(t *testing.T) {
	cipher, crypto := newCipher()
	bound := []seal.AssociatedData{seal.Bind("variable", "\xff"), seal.Bind("variable", "\xfe"), seal.Bind("variable", "\xff")}
	for _, each := range bound {
		if _, err := cipher.Seal(context.Background(), environment.TierProduction, each, []byte("v")); err != nil {
			t.Fatalf("Seal() err = %v", err)
		}
	}

	for i, sent := range crypto.contexts {
		value := sent["ocel:associated-data"]
		if len(sent) != 1 || !utf8.ValidString(value) {
			t.Fatalf("encryption context = %q, want one valid UTF-8 value: KMS takes a context as JSON strings, which replace each invalid byte and merge distinct associated data", sent)
		}
		decoded, err := base64.StdEncoding.DecodeString(value)
		if err != nil || !bytes.Equal(decoded, bound[i].Bytes()) {
			t.Errorf("encryption context %q decodes to %q, %v, want the associated data's bytes %q", value, decoded, err, bound[i].Bytes())
		}
	}
	if maps.Equal(crypto.contexts[0], crypto.contexts[1]) || !maps.Equal(crypto.contexts[0], crypto.contexts[2]) {
		t.Errorf("encryption contexts = %q, want the first and last alike and the middle one apart", crypto.contexts)
	}
}

func encodedContext(rendered string) string {
	return base64.StdEncoding.EncodeToString([]byte(rendered))
}

func TestAValueNamingNoTierIsRefusedRatherThanSealedUnderSomeKey(t *testing.T) {
	cipher, crypto := newCipher()

	_, err := cipher.Seal(context.Background(), "", seal.Bind("variable", "K"), []byte("v"))
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

func TestAnAccountWithNoBootstrapHasNoEntries(t *testing.T) {
	t.Parallel()

	store := awsports.KeyValues{Dynamo: newFakeDynamo()}
	name := stackrecords.ProjectKey(environment.TierProduction, "shop")

	if _, err := store.Read(context.Background(), name); !errors.Is(err, records.ErrNotFound) {
		t.Errorf("Read() with no bootstrap installed = %v, want ErrNotFound", err)
	}
	listed, err := store.List(context.Background(), stackrecords.ProjectsPartition(environment.TierProduction))
	if err != nil || len(listed) != 0 {
		t.Errorf("List() with no bootstrap installed = %v, %v, want nothing", listed, err)
	}
	if err := store.Remove(context.Background(), name, "whatever"); !errors.Is(err, records.ErrNotFound) {
		t.Errorf("Remove() with no bootstrap installed = %v, want ErrNotFound", err)
	}

	_, err = store.Write(context.Background(), records.Entry{Key: name, Value: []byte("{}")})
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady {
		t.Fatalf("Write() with no bootstrap installed = %v, want a %s refusal rather than a silent no-op", err, refusal.CodeNotReady)
	}
	if !strings.Contains(refused.Message, "ocel bootstrap") {
		t.Errorf("Write() refusal = %q, want it to name what creates the bootstrap", refused.Message)
	}
}

func TestATableDeletedMidTeardownHasNoEntries(t *testing.T) {
	t.Parallel()

	dynamo := newFakeDynamo()
	dynamo.gone = true
	store := awsports.KeyValues{Dynamo: dynamo, Tables: awsports.Table("ocel-bootstrap-state")}
	name := stackrecords.ProjectKey(environment.TierProduction, "shop")

	if _, err := store.Read(context.Background(), name); !errors.Is(err, records.ErrNotFound) {
		t.Errorf("Read() against a deleted table = %v, want ErrNotFound", err)
	}
	listed, err := store.List(context.Background(), stackrecords.ProjectsPartition(environment.TierProduction))
	if err != nil || len(listed) != 0 {
		t.Errorf("List() against a deleted table = %v, %v, want nothing", listed, err)
	}
	if err := store.Remove(context.Background(), name, "whatever"); !errors.Is(err, records.ErrNotFound) {
		t.Errorf("Remove() against a deleted table = %v, want ErrNotFound", err)
	}
	if _, err := store.Write(context.Background(), records.Entry{Key: name, Value: []byte("{}")}); err == nil {
		t.Error("Write() against a deleted table = nil, want the failure surfaced")
	}
}

func TestAPartitionIsItsRootAndPathAndNothingElse(t *testing.T) {
	for _, c := range []struct {
		in   records.Partition
		want string
	}{
		{stackrecords.ProjectsPartition(environment.TierProduction), "projects"},
		{stackrecords.StacksPartition(environment.TierProduction, "shop"), "stacks#shop"},
		{stackrecords.EnvironmentsPartition(environment.TierPreview, "shop"), "environments#shop"},
		{variablestore.ValuesPartition(variablestore.Scope{Project: "a#b", Tier: environment.TierPreview}), "values#a%23b"},
	} {
		if got := awsports.PartitionKey(c.in); got != c.want {
			t.Errorf("PartitionKey(%s) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestOneProjectsStacksDoNotShareAPartitionWithAnothers(t *testing.T) {
	shop := awsports.PartitionKey(stackrecords.StacksPartition(environment.TierProduction, "shop"))
	web := awsports.PartitionKey(stackrecords.StacksPartition(environment.TierProduction, "web"))
	if shop == web {
		t.Errorf("both projects' stacks land in %q, and one project's deploys then throttle the other's", shop)
	}
	if !strings.Contains(shop, "shop") {
		t.Errorf("stack partition = %q, want it to name the project whose deploys it contains", shop)
	}
}

func TestABindingsPairSharesOnePrefixInsideTheProjectPartition(t *testing.T) {
	table, ddb := newKeyValues(t)
	scope := variablestore.Scope{Project: "shop", Tier: environment.TierProduction}
	store := variablestore.Store{KeyValues: table, Cipher: newCipherIgnoringKMSCalls()}

	if _, err := store.SetBinding(context.Background(), scope, "", variablestore.OwnerOcel, "db",
		variablestore.BindingWrite{Record: []byte("{}"), Value: []byte("{}")}); err != nil {
		t.Fatalf("SetBinding err = %v", err)
	}

	partition := awsports.PartitionKey(variablestore.ValuesPartition(scope))
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

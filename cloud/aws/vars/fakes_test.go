package vars

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/kms"
)

// fakeKMS stands in for the account's variable key. Its "ciphertext" is the
// plaintext behind a marker, so a test can tell at a glance whether what
// reached the table was encrypted, and can assert that a read which was not
// asked to reveal never decrypted at all.
type fakeKMS struct {
	encrypts int
	decrypts int
	keyIDs   []string
}

const fakeCipherMarker = "enc:"

func (f *fakeKMS) Encrypt(_ context.Context, in *kms.EncryptInput, _ ...func(*kms.Options)) (*kms.EncryptOutput, error) {
	f.encrypts++
	f.keyIDs = append(f.keyIDs, aws.ToString(in.KeyId))
	return &kms.EncryptOutput{CiphertextBlob: append([]byte(fakeCipherMarker), in.Plaintext...)}, nil
}

func (f *fakeKMS) Decrypt(_ context.Context, in *kms.DecryptInput, _ ...func(*kms.Options)) (*kms.DecryptOutput, error) {
	f.decrypts++
	blob := string(in.CiphertextBlob)
	if !strings.HasPrefix(blob, fakeCipherMarker) {
		return nil, errors.New("fakeKMS: not ciphertext this key produced")
	}
	return &kms.DecryptOutput{Plaintext: []byte(strings.TrimPrefix(blob, fakeCipherMarker))}, nil
}

// fakeDynamo is an in-memory stand-in for the variables table, keyed the same
// way the real one is. It honours exactly the two access patterns the store
// emits — a point read and a prefix query — and exactly the one conditional
// write it emits, so an optimistic-concurrency failure in a test is a genuine
// condition failure rather than a canned error. TestSetEmitsTheOptimisticCondition
// pins the condition's text so this fake and the store cannot drift apart.
type fakeDynamo struct {
	items map[string]map[string]map[string]ddbtypes.AttributeValue

	transactions [][]ddbtypes.TransactWriteItem
	deletes      []*dynamodb.DeleteItemInput
	queries      []*dynamodb.QueryInput

	// beforeTransact runs between a store's read and its commit, so a test can
	// stage the interleaving only the table's own condition can catch.
	beforeTransact func()
}

func newFakeDynamo() *fakeDynamo {
	return &fakeDynamo{items: map[string]map[string]map[string]ddbtypes.AttributeValue{}}
}

func (f *fakeDynamo) get(pk, sk string) (map[string]ddbtypes.AttributeValue, bool) {
	item, ok := f.items[pk][sk]
	return item, ok
}

func (f *fakeDynamo) put(item map[string]ddbtypes.AttributeValue) {
	pk, sk := stringAttr(item, "pk"), stringAttr(item, "sk")
	if f.items[pk] == nil {
		f.items[pk] = map[string]map[string]ddbtypes.AttributeValue{}
	}
	f.items[pk][sk] = item
}

func (f *fakeDynamo) GetItem(_ context.Context, in *dynamodb.GetItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	item, ok := f.get(stringAttr(in.Key, "pk"), stringAttr(in.Key, "sk"))
	if !ok {
		return &dynamodb.GetItemOutput{}, nil
	}
	return &dynamodb.GetItemOutput{Item: item}, nil
}

func (f *fakeDynamo) DeleteItem(_ context.Context, in *dynamodb.DeleteItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error) {
	f.deletes = append(f.deletes, in)
	pk, sk := stringAttr(in.Key, "pk"), stringAttr(in.Key, "sk")
	out := &dynamodb.DeleteItemOutput{}
	if item, ok := f.get(pk, sk); ok {
		out.Attributes = item
		delete(f.items[pk], sk)
	}
	return out, nil
}

func (f *fakeDynamo) Query(_ context.Context, in *dynamodb.QueryInput, _ ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	f.queries = append(f.queries, in)

	pk := stringAttr(in.ExpressionAttributeValues, ":pk")
	prefix := stringAttr(in.ExpressionAttributeValues, ":prefix")

	var sks []string
	for sk := range f.items[pk] {
		if strings.HasPrefix(sk, prefix) {
			sks = append(sks, sk)
		}
	}
	sort.Strings(sks)
	if in.ScanIndexForward != nil && !*in.ScanIndexForward {
		sort.Sort(sort.Reverse(sort.StringSlice(sks)))
	}

	out := &dynamodb.QueryOutput{}
	for _, sk := range sks {
		out.Items = append(out.Items, f.items[pk][sk])
	}
	return out, nil
}

func (f *fakeDynamo) TransactWriteItems(_ context.Context, in *dynamodb.TransactWriteItemsInput, _ ...func(*dynamodb.Options)) (*dynamodb.TransactWriteItemsOutput, error) {
	f.transactions = append(f.transactions, in.TransactItems)
	if f.beforeTransact != nil {
		hook := f.beforeTransact
		f.beforeTransact = nil
		hook()
	}

	for _, w := range in.TransactItems {
		if w.Put == nil || w.Put.ConditionExpression == nil {
			continue
		}
		item, exists := f.get(stringAttr(w.Put.Item, "pk"), stringAttr(w.Put.Item, "sk"))
		seen := numberAttr(w.Put.ExpressionAttributeValues, ":seen")
		if exists && numberAttr(item, "version") != seen {
			return nil, &ddbtypes.TransactionCanceledException{}
		}
		if !exists && seen != 0 {
			return nil, &ddbtypes.TransactionCanceledException{}
		}
	}

	for _, w := range in.TransactItems {
		switch {
		case w.Put != nil:
			f.put(w.Put.Item)
		case w.Delete != nil:
			pk, sk := stringAttr(w.Delete.Key, "pk"), stringAttr(w.Delete.Key, "sk")
			delete(f.items[pk], sk)
		}
	}
	return &dynamodb.TransactWriteItemsOutput{}, nil
}

func stringAttr(m map[string]ddbtypes.AttributeValue, name string) string {
	if v, ok := m[name].(*ddbtypes.AttributeValueMemberS); ok {
		return v.Value
	}
	return ""
}

func numberAttr(m map[string]ddbtypes.AttributeValue, name string) int64 {
	v, ok := m[name].(*ddbtypes.AttributeValueMemberN)
	if !ok {
		return 0
	}
	var n int64
	for _, c := range v.Value {
		n = n*10 + int64(c-'0')
	}
	return n
}

func binaryAttr(m map[string]ddbtypes.AttributeValue, name string) []byte {
	if v, ok := m[name].(*ddbtypes.AttributeValueMemberB); ok {
		return v.Value
	}
	return nil
}

// newTestStore wires a store over the two fakes, with a clock that advances a
// second per write so version timestamps are distinguishable.
func newTestStore(t *testing.T) (*Store, *fakeDynamo, *fakeKMS) {
	t.Helper()
	ddb, crypto := newFakeDynamo(), &fakeKMS{}
	tick := time.Unix(1_700_000_000, 0)
	return &Store{
		Dynamo: ddb,
		KMS:    crypto,
		Table:  "ocel-vars",
		KeyARN: "arn:aws:kms:us-east-1:111122223333:key/abcd",
		Class:  "production",
		Now: func() time.Time {
			tick = tick.Add(time.Second)
			return tick
		},
	}, ddb, crypto
}

func testCoordinate() Coordinate {
	return Coordinate{Slug: "shop", Key: "STRIPE_API_KEY"}
}

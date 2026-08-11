package vars

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/kms"
)

type fakeKMS struct {
	mu       sync.Mutex
	encrypts int
	decrypts int
	keyIDs   []string
	contexts []map[string]string

	decryptContexts []map[string]string
}

const fakeCipherMarker = "enc:"

func sealedContext(ctx map[string]string) string {
	pairs := make([]string, 0, len(ctx))
	for k, v := range ctx {
		pairs = append(pairs, k+"="+v)
	}
	slices.Sort(pairs)
	return strings.Join(pairs, ",")
}

func (f *fakeKMS) Encrypt(_ context.Context, in *kms.EncryptInput, _ ...func(*kms.Options)) (*kms.EncryptOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.encrypts++
	f.keyIDs = append(f.keyIDs, aws.ToString(in.KeyId))
	f.contexts = append(f.contexts, in.EncryptionContext)
	blob := fakeCipherMarker + sealedContext(in.EncryptionContext) + "|" + string(in.Plaintext)
	return &kms.EncryptOutput{CiphertextBlob: []byte(blob)}, nil
}

func (f *fakeKMS) Decrypt(_ context.Context, in *kms.DecryptInput, _ ...func(*kms.Options)) (*kms.DecryptOutput, error) {
	f.mu.Lock()
	f.decrypts++
	f.decryptContexts = append(f.decryptContexts, in.EncryptionContext)
	f.mu.Unlock()
	blob := string(in.CiphertextBlob)
	if !strings.HasPrefix(blob, fakeCipherMarker) {
		return nil, errors.New("fakeKMS: not ciphertext this key produced")
	}
	sealed, plaintext, ok := strings.Cut(strings.TrimPrefix(blob, fakeCipherMarker), "|")
	if !ok {
		return nil, errors.New("fakeKMS: malformed ciphertext")
	}
	if presented := sealedContext(in.EncryptionContext); presented != sealed {
		return nil, fmt.Errorf("fakeKMS: encryption context %q does not match the one this blob was sealed under (%q)", presented, sealed)
	}
	return &kms.DecryptOutput{Plaintext: []byte(plaintext)}, nil
}

type fakeDynamo struct {
	items map[string]map[string]map[string]ddbtypes.AttributeValue

	transactions [][]ddbtypes.TransactWriteItem
	puts         []*dynamodb.PutItemInput
	queries      []*dynamodb.QueryInput

	beforeTransact func()
	beforePut      func()

	indexBehind bool
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

func (f *fakeDynamo) PutItem(_ context.Context, in *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	f.puts = append(f.puts, in)
	fire(&f.beforePut)

	if in.ConditionExpression != nil && !f.conditionHolds(in.Item, in.ExpressionAttributeValues) {
		return nil, &ddbtypes.ConditionalCheckFailedException{}
	}
	f.put(in.Item)
	return &dynamodb.PutItemOutput{}, nil
}

func (f *fakeDynamo) conditionHolds(item, values map[string]ddbtypes.AttributeValue) bool {
	current, exists := f.get(stringAttr(item, "pk"), stringAttr(item, "sk"))
	seen := numberAttr(values, ":seen")
	if !exists {
		return seen == 0
	}
	return numberAttr(current, "version") == seen
}

func fire(hook *func()) {
	if *hook == nil {
		return
	}
	run := *hook
	*hook = nil
	run()
}

func (f *fakeDynamo) Query(_ context.Context, in *dynamodb.QueryInput, _ ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	f.queries = append(f.queries, in)
	if in.IndexName != nil {
		return f.queryIndex(in)
	}

	pk := stringAttr(in.ExpressionAttributeValues, ":pk")
	prefix := stringAttr(in.ExpressionAttributeValues, ":prefix")

	var sks []string
	for sk := range f.items[pk] {
		if strings.HasPrefix(sk, prefix) {
			sks = append(sks, sk)
		}
	}
	slices.Sort(sks)
	if in.ScanIndexForward != nil && !*in.ScanIndexForward {
		slices.Reverse(sks)
	}

	out := &dynamodb.QueryOutput{}
	for _, sk := range sks {
		out.Items = append(out.Items, f.items[pk][sk])
	}
	return out, nil
}

func (f *fakeDynamo) queryIndex(in *dynamodb.QueryInput) (*dynamodb.QueryOutput, error) {
	if aws.ToString(in.IndexName) != IndexName {
		return nil, fmt.Errorf("fakeDynamo: no index named %q", aws.ToString(in.IndexName))
	}
	if f.indexBehind {
		return &dynamodb.QueryOutput{}, nil
	}
	gsi1pk := stringAttr(in.ExpressionAttributeValues, ":pk")
	gsi1sk := stringAttr(in.ExpressionAttributeValues, ":sk")

	var keys []string
	projected := map[string]map[string]ddbtypes.AttributeValue{}
	for pk, sks := range f.items {
		for sk, item := range sks {
			if stringAttr(item, "gsi1pk") != gsi1pk || stringAttr(item, "gsi1sk") != gsi1sk {
				continue
			}
			keys = append(keys, pk+"\x00"+sk)
			projected[pk+"\x00"+sk] = map[string]ddbtypes.AttributeValue{
				"pk": item["pk"], "sk": item["sk"], "gsi1pk": item["gsi1pk"], "gsi1sk": item["gsi1sk"],
			}
		}
	}
	slices.Sort(keys)

	out := &dynamodb.QueryOutput{}
	for _, key := range keys {
		out.Items = append(out.Items, projected[key])
	}
	return out, nil
}

func (f *fakeDynamo) TransactWriteItems(_ context.Context, in *dynamodb.TransactWriteItemsInput, _ ...func(*dynamodb.Options)) (*dynamodb.TransactWriteItemsOutput, error) {
	f.transactions = append(f.transactions, in.TransactItems)
	fire(&f.beforeTransact)

	for _, w := range in.TransactItems {
		if w.Put == nil || w.Put.ConditionExpression == nil {
			continue
		}
		if !f.conditionHolds(w.Put.Item, w.Put.ExpressionAttributeValues) {
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

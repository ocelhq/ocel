package vars

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
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
	mu sync.Mutex

	items map[string]map[string]map[string]ddbtypes.AttributeValue

	transactions [][]ddbtypes.TransactWriteItem
	puts         []*dynamodb.PutItemInput
	queries      []*dynamodb.QueryInput

	beforeTransact  func()
	beforePut       func(*dynamodb.PutItemInput)
	beforePutSK     string
	afterQuery      func()
	cancelTransacts int

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
	f.mu.Lock()
	defer f.mu.Unlock()
	item, ok := f.get(stringAttr(in.Key, "pk"), stringAttr(in.Key, "sk"))
	if !ok {
		return &dynamodb.GetItemOutput{}, nil
	}
	return &dynamodb.GetItemOutput{Item: item}, nil
}

func (f *fakeDynamo) PutItem(_ context.Context, in *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	f.mu.Lock()
	f.puts = append(f.puts, in)
	var hook func(*dynamodb.PutItemInput)
	if f.beforePut != nil && (f.beforePutSK == "" || f.beforePutSK == stringAttr(in.Item, "sk")) {
		hook, f.beforePut, f.beforePutSK = f.beforePut, nil, ""
	}
	f.mu.Unlock()

	if hook != nil {
		hook(in)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
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

func (f *fakeDynamo) armBeforePutTo(sk string, hook func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.beforePutSK = sk
	f.beforePut = func(*dynamodb.PutItemInput) { hook() }
}

func (f *fakeDynamo) fireTransact() {
	f.mu.Lock()
	hook := f.beforeTransact
	f.beforeTransact = nil
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
}

func (f *fakeDynamo) Query(_ context.Context, in *dynamodb.QueryInput, _ ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
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
	if f.afterQuery != nil {
		hook := f.afterQuery
		f.afterQuery = nil
		hook()
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
	f.mu.Lock()
	f.transactions = append(f.transactions, in.TransactItems)
	f.mu.Unlock()

	f.fireTransact()

	f.mu.Lock()
	defer f.mu.Unlock()

	if f.cancelTransacts > 0 {
		f.cancelTransacts--
		return nil, &ddbtypes.TransactionCanceledException{}
	}

	touched := map[string]bool{}
	for _, w := range in.TransactItems {
		key, condition, values := transactTarget(w)
		if touched[cellKey(stringAttr(key, "pk"), stringAttr(key, "sk"))] {
			return nil, fmt.Errorf("fakeDynamo: %s/%s appears twice in one transaction", stringAttr(key, "pk"), stringAttr(key, "sk"))
		}
		touched[cellKey(stringAttr(key, "pk"), stringAttr(key, "sk"))] = true
		if condition != nil && !f.conditionHolds(key, values) {
			return nil, &ddbtypes.TransactionCanceledException{}
		}
	}

	staged := make([]map[string]ddbtypes.AttributeValue, 0, len(in.TransactItems))
	var removed []map[string]ddbtypes.AttributeValue
	for _, w := range in.TransactItems {
		switch {
		case w.Put != nil:
			staged = append(staged, w.Put.Item)
		case w.Delete != nil:
			removed = append(removed, w.Delete.Key)
		case w.Update != nil:
			updated, err := f.applyUpdate(w.Update)
			if err != nil {
				return nil, err
			}
			staged = append(staged, updated)
		}
	}

	for _, item := range staged {
		f.put(item)
	}
	for _, key := range removed {
		delete(f.items[stringAttr(key, "pk")], stringAttr(key, "sk"))
	}
	return &dynamodb.TransactWriteItemsOutput{}, nil
}

func transactTarget(w ddbtypes.TransactWriteItem) (map[string]ddbtypes.AttributeValue, *string, map[string]ddbtypes.AttributeValue) {
	switch {
	case w.Put != nil:
		return w.Put.Item, w.Put.ConditionExpression, w.Put.ExpressionAttributeValues
	case w.Delete != nil:
		return w.Delete.Key, w.Delete.ConditionExpression, w.Delete.ExpressionAttributeValues
	case w.Update != nil:
		return w.Update.Key, w.Update.ConditionExpression, w.Update.ExpressionAttributeValues
	}
	return nil, nil, nil
}

func (f *fakeDynamo) applyUpdate(u *ddbtypes.Update) (map[string]ddbtypes.AttributeValue, error) {
	item := map[string]ddbtypes.AttributeValue{}
	if current, ok := f.get(stringAttr(u.Key, "pk"), stringAttr(u.Key, "sk")); ok {
		maps.Copy(item, current)
	}
	maps.Copy(item, u.Key)

	for clause, assignments := range updateClauses(aws.ToString(u.UpdateExpression)) {
		for _, assignment := range assignments {
			name, value, err := resolveAssignment(clause, assignment, u.ExpressionAttributeNames, u.ExpressionAttributeValues)
			if err != nil {
				return nil, err
			}
			if clause == "ADD" {
				delta, ok := value.(*ddbtypes.AttributeValueMemberN)
				if !ok {
					return nil, fmt.Errorf("fakeDynamo: ADD %s takes a number", name)
				}
				n, err := strconv.ParseInt(delta.Value, 10, 64)
				if err != nil {
					return nil, err
				}
				value = &ddbtypes.AttributeValueMemberN{Value: strconv.FormatInt(numberAttr(item, name)+n, 10)}
			}
			item[name] = value
		}
	}
	return item, nil
}

func updateClauses(expression string) map[string][]string {
	sets, adds, added := strings.Cut(expression, " ADD ")
	out := map[string][]string{"SET": strings.Split(strings.TrimPrefix(sets, "SET "), ", ")}
	if added {
		out["ADD"] = strings.Split(adds, ", ")
	}
	return out
}

func resolveAssignment(clause, assignment string, names map[string]string, values map[string]ddbtypes.AttributeValue) (string, ddbtypes.AttributeValue, error) {
	separator := " "
	if clause == "SET" {
		separator = " = "
	}
	alias, placeholder, ok := strings.Cut(assignment, separator)
	if !ok {
		return "", nil, fmt.Errorf("fakeDynamo: %s clause %q is not one this double understands", clause, assignment)
	}
	name, ok := names[alias]
	if !ok {
		return "", nil, fmt.Errorf("fakeDynamo: %q names no attribute", alias)
	}
	value, ok := values[placeholder]
	if !ok {
		return "", nil, fmt.Errorf("fakeDynamo: %q carries no value", placeholder)
	}
	return name, value, nil
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
	var clock sync.Mutex
	tick := time.Unix(1_700_000_000, 0)
	return &Store{
		Dynamo: ddb,
		KMS:    crypto,
		Table:  "ocel-vars",
		KeyARN: "arn:aws:kms:us-east-1:111122223333:key/abcd",
		Class:  "production",
		Now: func() time.Time {
			clock.Lock()
			defer clock.Unlock()
			tick = tick.Add(time.Second)
			return tick
		},
	}, ddb, crypto
}

func testCoordinate() Coordinate {
	return Coordinate{Slug: "shop", Key: "STRIPE_API_KEY"}
}

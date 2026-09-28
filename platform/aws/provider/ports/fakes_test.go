package ports_test

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/kms"
)

const fakePageSize = 2

type fakeDynamo struct {
	mu     sync.Mutex
	gone   bool
	items  map[string]map[string]map[string]map[string]ddbtypes.AttributeValue
	tables map[string]bool
}

func (f *fakeDynamo) missing() error {
	if f.gone {
		return &ddbtypes.ResourceNotFoundException{Message: aws.String("Requested resource not found")}
	}
	return nil
}

func newFakeDynamo() *fakeDynamo {
	return &fakeDynamo{items: map[string]map[string]map[string]map[string]ddbtypes.AttributeValue{}}
}

func (f *fakeDynamo) GetItem(_ context.Context, in *dynamodb.GetItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	if err := f.missing(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sawTable(in.TableName)
	item, ok := f.table(in.TableName)[stringAttr(in.Key, "pk")][stringAttr(in.Key, "sk")]
	if !ok {
		return &dynamodb.GetItemOutput{}, nil
	}
	return &dynamodb.GetItemOutput{Item: maps.Clone(item)}, nil
}

func (f *fakeDynamo) PutItem(_ context.Context, in *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	if err := f.missing(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sawTable(in.TableName)
	if err := namesAndValuesUsed(in.ExpressionAttributeNames, in.ExpressionAttributeValues, aws.ToString(in.ConditionExpression)); err != nil {
		return nil, err
	}
	pk, sk := stringAttr(in.Item, "pk"), stringAttr(in.Item, "sk")
	items := f.table(in.TableName)
	existing, exists := items[pk][sk]
	if !f.satisfies(aws.ToString(in.ConditionExpression), existing, exists, in.ExpressionAttributeValues) {
		return nil, &ddbtypes.ConditionalCheckFailedException{Message: aws.String("fakeDynamo: the condition did not hold")}
	}
	if items[pk] == nil {
		items[pk] = map[string]map[string]ddbtypes.AttributeValue{}
	}
	items[pk][sk] = maps.Clone(in.Item)
	return &dynamodb.PutItemOutput{}, nil
}

func (f *fakeDynamo) DeleteItem(_ context.Context, in *dynamodb.DeleteItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error) {
	if err := f.missing(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sawTable(in.TableName)
	if err := namesAndValuesUsed(in.ExpressionAttributeNames, in.ExpressionAttributeValues, aws.ToString(in.ConditionExpression)); err != nil {
		return nil, err
	}
	pk, sk := stringAttr(in.Key, "pk"), stringAttr(in.Key, "sk")
	items := f.table(in.TableName)
	existing, exists := items[pk][sk]
	if !f.satisfies(aws.ToString(in.ConditionExpression), existing, exists, in.ExpressionAttributeValues) {
		failed := &ddbtypes.ConditionalCheckFailedException{Message: aws.String("fakeDynamo: the condition did not hold")}
		if exists && in.ReturnValuesOnConditionCheckFailure == ddbtypes.ReturnValuesOnConditionCheckFailureAllOld {
			failed.Item = maps.Clone(existing)
		}
		return nil, failed
	}
	delete(items[pk], sk)
	return &dynamodb.DeleteItemOutput{}, nil
}

func (f *fakeDynamo) TransactWriteItems(_ context.Context, in *dynamodb.TransactWriteItemsInput, _ ...func(*dynamodb.Options)) (*dynamodb.TransactWriteItemsOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, write := range in.TransactItems {
		if write.Put != nil {
			f.sawTable(write.Put.TableName)
		}
		if write.Put == nil {
			return nil, fmt.Errorf("fakeDynamo: this transaction includes an operation that is not a put")
		}
		if err := namesAndValuesUsed(write.Put.ExpressionAttributeNames, write.Put.ExpressionAttributeValues, aws.ToString(write.Put.ConditionExpression)); err != nil {
			return nil, err
		}
		pk, sk := stringAttr(write.Put.Item, "pk"), stringAttr(write.Put.Item, "sk")
		existing, exists := f.table(write.Put.TableName)[pk][sk]
		if f.satisfies(aws.ToString(write.Put.ConditionExpression), existing, exists, write.Put.ExpressionAttributeValues) {
			continue
		}
		return nil, &ddbtypes.TransactionCanceledException{
			Message:             aws.String("fakeDynamo: the transaction was cancelled"),
			CancellationReasons: []ddbtypes.CancellationReason{{Code: aws.String("ConditionalCheckFailed")}},
		}
	}
	for _, write := range in.TransactItems {
		pk, sk := stringAttr(write.Put.Item, "pk"), stringAttr(write.Put.Item, "sk")
		items := f.table(write.Put.TableName)
		if items[pk] == nil {
			items[pk] = map[string]map[string]ddbtypes.AttributeValue{}
		}
		items[pk][sk] = maps.Clone(write.Put.Item)
	}
	return &dynamodb.TransactWriteItemsOutput{}, nil
}

func (f *fakeDynamo) satisfies(expression string, existing map[string]ddbtypes.AttributeValue, exists bool, values map[string]ddbtypes.AttributeValue) bool {
	switch expression {
	case "":
		return true
	case "attribute_not_exists(#pk)":
		return !exists
	case "#rev = :rev":
		return exists && stringAttr(existing, "rev") == stringAttr(values, ":rev")
	}
	panic("fakeDynamo: unrecognized condition " + expression)
}

func namesAndValuesUsed(names map[string]string, values map[string]ddbtypes.AttributeValue, expression string) error {
	for _, key := range slices.Sorted(maps.Keys(names)) {
		if !strings.Contains(expression, key) {
			return fmt.Errorf("fakeDynamo: ValidationException: Value provided in ExpressionAttributeNames unused in expressions: keys: {%s}", key)
		}
	}
	for _, key := range slices.Sorted(maps.Keys(values)) {
		if !strings.Contains(expression, key) {
			return fmt.Errorf("fakeDynamo: ValidationException: Value provided in ExpressionAttributeValues unused in expressions: keys: {%s}", key)
		}
	}
	return nil
}

func (f *fakeDynamo) Query(_ context.Context, in *dynamodb.QueryInput, _ ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	if err := f.missing(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sawTable(in.TableName)
	if err := namesAndValuesUsed(in.ExpressionAttributeNames, in.ExpressionAttributeValues, aws.ToString(in.KeyConditionExpression)); err != nil {
		return nil, err
	}
	switch got := aws.ToString(in.KeyConditionExpression); got {
	case "#pk = :pk", "#pk = :pk AND begins_with(#sk, :prefix)":
	default:
		return nil, fmt.Errorf("fakeDynamo: unrecognized key condition %q", got)
	}
	pk, prefix := stringAttr(in.ExpressionAttributeValues, ":pk"), stringAttr(in.ExpressionAttributeValues, ":prefix")
	if prefix == "" && strings.Contains(aws.ToString(in.KeyConditionExpression), "begins_with") {
		return nil, fmt.Errorf("fakeDynamo: DynamoDB refuses an empty value for the key attribute sk")
	}

	items := f.table(in.TableName)
	var sks []string
	for sk := range items[pk] {
		if strings.HasPrefix(sk, prefix) {
			sks = append(sks, sk)
		}
	}
	slices.Sort(sks)
	if after := stringAttr(in.ExclusiveStartKey, "sk"); after != "" {
		sks = sks[min(len(sks), slices.Index(sks, after)+1):]
	}

	out := &dynamodb.QueryOutput{}
	for i, sk := range sks {
		if i == fakePageSize {
			out.LastEvaluatedKey = map[string]ddbtypes.AttributeValue{
				"pk": &ddbtypes.AttributeValueMemberS{Value: pk},
				"sk": &ddbtypes.AttributeValueMemberS{Value: sks[i-1]},
			}
			break
		}
		out.Items = append(out.Items, maps.Clone(items[pk][sk]))
	}
	return out, nil
}

func (f *fakeDynamo) table(name *string) map[string]map[string]map[string]ddbtypes.AttributeValue {
	items := f.items[aws.ToString(name)]
	if items == nil {
		items = map[string]map[string]map[string]ddbtypes.AttributeValue{}
		f.items[aws.ToString(name)] = items
	}
	return items
}

func (f *fakeDynamo) partitions() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var pks []string
	for _, items := range f.items {
		pks = append(pks, slices.Collect(maps.Keys(items))...)
	}
	slices.Sort(pks)
	return slices.Compact(pks)
}

func (f *fakeDynamo) sortKeys(pk string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var sks []string
	for _, items := range f.items {
		sks = append(sks, slices.Collect(maps.Keys(items[pk]))...)
	}
	slices.Sort(sks)
	return slices.Compact(sks)
}

func (f *fakeDynamo) item(pk, sk string) map[string]ddbtypes.AttributeValue {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, items := range f.items {
		if item, ok := items[pk][sk]; ok {
			return maps.Clone(item)
		}
	}
	return nil
}

func stringAttr(item map[string]ddbtypes.AttributeValue, name string) string {
	value, ok := item[name].(*ddbtypes.AttributeValueMemberS)
	if !ok {
		return ""
	}
	return value.Value
}

const fakeCipherMarker = "enc:"

type fakeKMS struct {
	mu       sync.Mutex
	keyIDs   []string
	contexts []map[string]string
}

func (f *fakeKMS) Encrypt(_ context.Context, in *kms.EncryptInput, _ ...func(*kms.Options)) (*kms.EncryptOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keyIDs = append(f.keyIDs, aws.ToString(in.KeyId))
	f.contexts = append(f.contexts, in.EncryptionContext)
	blob := fakeCipherMarker + aws.ToString(in.KeyId) + "#" + sealedContext(in.EncryptionContext) + "|" + base64.StdEncoding.EncodeToString(in.Plaintext)
	return &kms.EncryptOutput{CiphertextBlob: []byte(blob)}, nil
}

func (f *fakeKMS) Decrypt(_ context.Context, in *kms.DecryptInput, _ ...func(*kms.Options)) (*kms.DecryptOutput, error) {
	blob := string(in.CiphertextBlob)
	if !strings.HasPrefix(blob, fakeCipherMarker) {
		return nil, errors.New("fakeKMS: not ciphertext this key produced")
	}
	sealed, plaintext, ok := strings.Cut(strings.TrimPrefix(blob, fakeCipherMarker), "|")
	if !ok {
		return nil, errors.New("fakeKMS: malformed ciphertext")
	}
	key, sealed, ok := strings.Cut(sealed, "#")
	if !ok {
		return nil, errors.New("fakeKMS: malformed ciphertext")
	}
	if key != aws.ToString(in.KeyId) {
		return nil, fmt.Errorf("fakeKMS: key %q did not seal this blob (%q did)", aws.ToString(in.KeyId), key)
	}
	if presented := sealedContext(in.EncryptionContext); presented != sealed {
		return nil, fmt.Errorf("fakeKMS: encryption context %q does not match the one this blob was sealed under (%q)", presented, sealed)
	}
	opened, err := base64.StdEncoding.DecodeString(plaintext)
	if err != nil {
		return nil, fmt.Errorf("fakeKMS: malformed ciphertext: %w", err)
	}
	return &kms.DecryptOutput{Plaintext: opened}, nil
}

func sealedContext(bound map[string]string) string {
	pairs := make([]string, 0, len(bound))
	for k, v := range bound {
		pairs = append(pairs, k+"="+v)
	}
	slices.Sort(pairs)
	return strings.Join(pairs, ",")
}

func (f *fakeDynamo) sawTable(name *string) {
	if f.tables == nil {
		f.tables = map[string]bool{}
	}
	f.tables[aws.ToString(name)] = true
}

func (f *fakeDynamo) tablesUsed() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Sorted(maps.Keys(f.tables))
}

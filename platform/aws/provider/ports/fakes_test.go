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
	mu    sync.Mutex
	items map[string]map[string]map[string]ddbtypes.AttributeValue
}

func newFakeDynamo() *fakeDynamo {
	return &fakeDynamo{items: map[string]map[string]map[string]ddbtypes.AttributeValue{}}
}

func (f *fakeDynamo) GetItem(_ context.Context, in *dynamodb.GetItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	item, ok := f.items[stringAttr(in.Key, "pk")][stringAttr(in.Key, "sk")]
	if !ok {
		return &dynamodb.GetItemOutput{}, nil
	}
	return &dynamodb.GetItemOutput{Item: maps.Clone(item)}, nil
}

func (f *fakeDynamo) PutItem(_ context.Context, in *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	pk, sk := stringAttr(in.Item, "pk"), stringAttr(in.Item, "sk")
	held, exists := f.items[pk][sk]
	if !f.holds(aws.ToString(in.ConditionExpression), held, exists, in.ExpressionAttributeValues) {
		return nil, &ddbtypes.ConditionalCheckFailedException{Message: aws.String("fakeDynamo: the condition did not hold")}
	}
	if f.items[pk] == nil {
		f.items[pk] = map[string]map[string]ddbtypes.AttributeValue{}
	}
	f.items[pk][sk] = maps.Clone(in.Item)
	return &dynamodb.PutItemOutput{}, nil
}

func (f *fakeDynamo) DeleteItem(_ context.Context, in *dynamodb.DeleteItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	pk, sk := stringAttr(in.Key, "pk"), stringAttr(in.Key, "sk")
	held, exists := f.items[pk][sk]
	if !f.holds(aws.ToString(in.ConditionExpression), held, exists, in.ExpressionAttributeValues) {
		failed := &ddbtypes.ConditionalCheckFailedException{Message: aws.String("fakeDynamo: the condition did not hold")}
		if exists && in.ReturnValuesOnConditionCheckFailure == ddbtypes.ReturnValuesOnConditionCheckFailureAllOld {
			failed.Item = maps.Clone(held)
		}
		return nil, failed
	}
	delete(f.items[pk], sk)
	return &dynamodb.DeleteItemOutput{}, nil
}

func (f *fakeDynamo) holds(expression string, held map[string]ddbtypes.AttributeValue, exists bool, values map[string]ddbtypes.AttributeValue) bool {
	switch expression {
	case "":
		return true
	case "attribute_not_exists(#pk)":
		return !exists
	case "#rev = :rev":
		return exists && stringAttr(held, "rev") == stringAttr(values, ":rev")
	}
	panic("fakeDynamo: unrecognized condition " + expression)
}

func (f *fakeDynamo) Query(_ context.Context, in *dynamodb.QueryInput, _ ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if got := aws.ToString(in.KeyConditionExpression); got != "#pk = :pk AND begins_with(#sk, :prefix)" {
		return nil, fmt.Errorf("fakeDynamo: unrecognized key condition %q", got)
	}
	pk, prefix := stringAttr(in.ExpressionAttributeValues, ":pk"), stringAttr(in.ExpressionAttributeValues, ":prefix")

	var sks []string
	for sk := range f.items[pk] {
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
		out.Items = append(out.Items, maps.Clone(f.items[pk][sk]))
	}
	return out, nil
}

func (f *fakeDynamo) partitions() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Sorted(maps.Keys(f.items))
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
	blob := fakeCipherMarker + sealedContext(in.EncryptionContext) + "|" + base64.StdEncoding.EncodeToString(in.Plaintext)
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

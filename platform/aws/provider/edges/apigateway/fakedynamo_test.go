package apigateway

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

type fakeDynamo struct {
	mu       sync.Mutex
	items    map[string]map[string]ddbtypes.AttributeValue
	calls    []string
	pageSize int

	putErr    error
	beforePut func(key string, items map[string]map[string]ddbtypes.AttributeValue)
}

func newFakeDynamo() *fakeDynamo {
	return &fakeDynamo{items: map[string]map[string]ddbtypes.AttributeValue{}, pageSize: 1}
}

func dynamoKey(item map[string]ddbtypes.AttributeValue) string {
	pk, _ := item["pk"].(*ddbtypes.AttributeValueMemberS)
	sk, _ := item["sk"].(*ddbtypes.AttributeValueMemberS)
	if pk == nil || sk == nil {
		return ""
	}
	return pk.Value + "\x00" + sk.Value
}

func (f *fakeDynamo) GetItem(_ context.Context, in *dynamodb.GetItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return &dynamodb.GetItemOutput{Item: f.items[dynamoKey(in.Key)]}, nil
}

func (f *fakeDynamo) PutItem(_ context.Context, in *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.putErr != nil {
		return nil, f.putErr
	}
	key := dynamoKey(in.Item)
	if f.beforePut != nil {
		f.beforePut(key, f.items)
	}
	met, err := conditionHolds(in, f.items[key])
	if err != nil {
		return nil, err
	}
	if !met {
		return nil, &ddbtypes.ConditionalCheckFailedException{Message: aws.String("condition on " + key)}
	}
	f.calls = append(f.calls, "PutItem "+key)
	f.items[key] = in.Item
	return &dynamodb.PutItemOutput{}, nil
}

func conditionHolds(in *dynamodb.PutItemInput, present map[string]ddbtypes.AttributeValue) (bool, error) {
	condition := aws.ToString(in.ConditionExpression)
	switch condition {
	case "":
		return true, nil
	case "attribute_not_exists(#pk)":
		return len(present) == 0, nil
	case "#rev = :rev":
		want, ok := in.ExpressionAttributeValues[":rev"].(*ddbtypes.AttributeValueMemberS)
		if !ok {
			return false, fmt.Errorf("fake dynamo needs the revision the condition %q compares against", condition)
		}
		got, ok := present["rev"].(*ddbtypes.AttributeValueMemberS)
		return ok && got.Value == want.Value, nil
	default:
		return false, fmt.Errorf("fake dynamo does not speak the condition %q", condition)
	}
}

func (f *fakeDynamo) TransactWriteItems(context.Context, *dynamodb.TransactWriteItemsInput, ...func(*dynamodb.Options)) (*dynamodb.TransactWriteItemsOutput, error) {
	return nil, fmt.Errorf("fake dynamo writes no transaction: nothing this suite drives writes a pair")
}

func (f *fakeDynamo) DeleteItem(_ context.Context, in *dynamodb.DeleteItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := dynamoKey(in.Key)
	item, present := f.items[key]
	if aws.ToString(in.ConditionExpression) != "" {
		want, ok := in.ExpressionAttributeValues[":rev"].(*ddbtypes.AttributeValueMemberS)
		if !ok {
			return nil, fmt.Errorf("fake dynamo needs the revision the condition %q compares against", aws.ToString(in.ConditionExpression))
		}
		got, matched := item["rev"].(*ddbtypes.AttributeValueMemberS)
		if !present {
			return nil, &ddbtypes.ConditionalCheckFailedException{Message: aws.String("no " + key)}
		}
		if !matched || got.Value != want.Value {
			return nil, &ddbtypes.ConditionalCheckFailedException{Message: aws.String("condition on " + key), Item: item}
		}
	}
	f.calls = append(f.calls, "DeleteItem "+key)
	delete(f.items, key)
	return &dynamodb.DeleteItemOutput{}, nil
}

func (f *fakeDynamo) Query(_ context.Context, in *dynamodb.QueryInput, _ ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	condition := aws.ToString(in.KeyConditionExpression)
	pk, _ := in.ExpressionAttributeValues[":pk"].(*ddbtypes.AttributeValueMemberS)
	if pk == nil {
		return nil, fmt.Errorf("fake dynamo needs a partition, got %q", condition)
	}
	prefix := ""
	switch condition {
	case "#pk = :pk":
	case "#pk = :pk AND begins_with(#sk, :prefix)":
		value, _ := in.ExpressionAttributeValues[":prefix"].(*ddbtypes.AttributeValueMemberS)
		if value == nil {
			return nil, fmt.Errorf("fake dynamo needs a prefix for %q", condition)
		}
		prefix = value.Value
	default:
		return nil, fmt.Errorf("fake dynamo does not speak the key condition %q", condition)
	}

	var items []map[string]ddbtypes.AttributeValue
	for _, key := range slices.Sorted(maps.Keys(f.items)) {
		partition, sort, _ := strings.Cut(key, "\x00")
		if partition != pk.Value || !strings.HasPrefix(sort, prefix) {
			continue
		}
		if after := dynamoKey(in.ExclusiveStartKey); after != "" && key <= after {
			continue
		}
		items = append(items, f.items[key])
		if len(items) == f.pageSize {
			return &dynamodb.QueryOutput{Items: items, LastEvaluatedKey: lastKey(f.items[key])}, nil
		}
	}
	return &dynamodb.QueryOutput{Items: items}, nil
}

func lastKey(item map[string]ddbtypes.AttributeValue) map[string]ddbtypes.AttributeValue {
	return map[string]ddbtypes.AttributeValue{"pk": item["pk"], "sk": item["sk"]}
}

package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/ocelhq/ocel/pkg/provider"
)

type recordItem struct {
	PK            string `dynamodbav:"pk"`
	SK            string `dynamodbav:"sk"`
	Value         string `dynamodbav:"value"`
	UntilMillis   int64  `dynamodbav:"record_until"`
	ExpiresAtUnix int64  `dynamodbav:"expires_at"`
}

func (s store) recordPartition(purpose provider.RecordPurpose, topic string) string {
	return s.prefix + "record#" + string(purpose) + "#" + topic
}

func (s store) recordItemOf(record provider.ExpiringRecord) recordItem {
	return recordItem{
		PK:            s.recordPartition(record.Purpose, record.Topic),
		SK:            record.Key,
		Value:         string(record.Value),
		UntilMillis:   record.ExpiresAt.UnixMilli(),
		ExpiresAtUnix: record.ExpiresAt.Add(time.Hour).Unix(),
	}
}

func (s store) EnsureRecord(ctx context.Context, record provider.ExpiringRecord) (provider.ExpiringRecord, bool, error) {
	item := s.recordItemOf(record)
	encoded, err := attributevalue.MarshalMap(item)
	if err != nil {
		return provider.ExpiringRecord{}, false, err
	}
	_, err = s.db.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:                 aws.String(s.table),
		Item:                      encoded,
		ConditionExpression:       aws.String("attribute_not_exists(pk) OR record_until <= :now"),
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{":now": &ddbtypes.AttributeValueMemberN{Value: strconv.FormatInt(time.Now().UnixMilli(), 10)}},
	})
	if err == nil {
		return record, true, nil
	}
	if !isConditionFailed(err) {
		return provider.ExpiringRecord{}, false, fmt.Errorf("record the %s key %q of %s: %w", record.Purpose, record.Key, record.Topic, err)
	}
	out, err := s.db.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:      aws.String(s.table),
		Key:            stringKey(item.PK, item.SK),
		ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return provider.ExpiringRecord{}, false, fmt.Errorf("read the %s key %q of %s: %w", record.Purpose, record.Key, record.Topic, err)
	}
	var held recordItem
	if err := attributevalue.UnmarshalMap(out.Item, &held); err != nil {
		return provider.ExpiringRecord{}, false, err
	}
	record.Value = json.RawMessage(held.Value)
	record.ExpiresAt = time.UnixMilli(held.UntilMillis)
	return record, false, nil
}

func (s store) rewriteRecord(ctx context.Context, record provider.ExpiringRecord) error {
	encoded, err := attributevalue.MarshalMap(s.recordItemOf(record))
	if err != nil {
		return err
	}
	if _, err := s.db.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String(s.table), Item: encoded}); err != nil {
		return fmt.Errorf("record the %s key %q of %s: %w", record.Purpose, record.Key, record.Topic, err)
	}
	return nil
}

func (s store) deleteRecord(ctx context.Context, purpose provider.RecordPurpose, topic, key string) error {
	if _, err := s.db.DeleteItem(ctx, &dynamodb.DeleteItemInput{TableName: aws.String(s.table), Key: stringKey(s.recordPartition(purpose, topic), key)}); err != nil {
		return fmt.Errorf("delete the %s key %q of %s: %w", purpose, key, topic, err)
	}
	return nil
}

func stringKey(pk, sk string) map[string]ddbtypes.AttributeValue {
	return map[string]ddbtypes.AttributeValue{
		keyPK: &ddbtypes.AttributeValueMemberS{Value: pk},
		keySK: &ddbtypes.AttributeValueMemberS{Value: sk},
	}
}

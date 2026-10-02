package tasks

import (
	"context"
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
	PK        string `dynamodbav:"pk"`
	SK        string `dynamodbav:"sk"`
	Value     string `dynamodbav:"value"`
	Until     int64  `dynamodbav:"record_until"`
	Retention int64  `dynamodbav:"expires_at"`
}

func (s store) recordPartition(purpose provider.RecordPurpose, topic string) string {
	return s.prefix + "record#" + string(purpose) + "#" + topic
}

func (s store) ensureRecord(ctx context.Context, purpose provider.RecordPurpose, topic, key, value string, until time.Time) (string, bool, error) {
	held, created, err := s.holdRecord(ctx, purpose, topic, key, value, until)
	return held.Value, created, err
}

func (s store) holdRecord(ctx context.Context, purpose provider.RecordPurpose, topic, key, value string, until time.Time) (recordItem, bool, error) {
	now := time.Now()
	item := recordItem{PK: s.recordPartition(purpose, topic), SK: key, Value: value, Until: until.UnixMilli(), Retention: until.Add(time.Hour).Unix()}
	encoded, err := attributevalue.MarshalMap(item)
	if err != nil {
		return recordItem{}, false, err
	}
	_, err = s.db.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:                 aws.String(s.table),
		Item:                      encoded,
		ConditionExpression:       aws.String("attribute_not_exists(pk) OR record_until <= :now"),
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{":now": &ddbtypes.AttributeValueMemberN{Value: strconv.FormatInt(now.UnixMilli(), 10)}},
	})
	if err == nil {
		return item, true, nil
	}
	if !isConditionFailed(err) {
		return recordItem{}, false, fmt.Errorf("record the %s key %q of %s: %w", purpose, key, topic, err)
	}
	out, err := s.db.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:      aws.String(s.table),
		Key:            stringKey(item.PK, item.SK),
		ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return recordItem{}, false, fmt.Errorf("read the %s key %q of %s: %w", purpose, key, topic, err)
	}
	var held recordItem
	if err := attributevalue.UnmarshalMap(out.Item, &held); err != nil {
		return recordItem{}, false, err
	}
	return held, false, nil
}

func (s store) rewriteRecord(ctx context.Context, purpose provider.RecordPurpose, topic, key, value string, until time.Time) error {
	encoded, err := attributevalue.MarshalMap(recordItem{PK: s.recordPartition(purpose, topic), SK: key, Value: value, Until: until.UnixMilli(), Retention: until.Add(time.Hour).Unix()})
	if err != nil {
		return err
	}
	if _, err := s.db.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String(s.table), Item: encoded}); err != nil {
		return fmt.Errorf("record the %s key %q of %s: %w", purpose, key, topic, err)
	}
	return nil
}

func (s store) forgetRecord(ctx context.Context, purpose provider.RecordPurpose, topic, key string) error {
	if _, err := s.db.DeleteItem(ctx, &dynamodb.DeleteItemInput{TableName: aws.String(s.table), Key: stringKey(s.recordPartition(purpose, topic), key)}); err != nil {
		return fmt.Errorf("forget the %s key %q of %s: %w", purpose, key, topic, err)
	}
	return nil
}

func stringKey(pk, sk string) map[string]ddbtypes.AttributeValue {
	return map[string]ddbtypes.AttributeValue{
		keyPK: &ddbtypes.AttributeValueMemberS{Value: pk},
		keySK: &ddbtypes.AttributeValueMemberS{Value: sk},
	}
}

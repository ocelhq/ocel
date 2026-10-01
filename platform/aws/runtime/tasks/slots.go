package tasks

import (
	"context"
	"fmt"
	"math/rand/v2"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

const maxSlotTries = 8

type slotSet struct {
	name  string
	limit int
}

func (s store) slotPartition(set string) string { return s.prefix + "slot#" + set }

func (s store) takeSlot(ctx context.Context, set slotSet, holder string, until time.Time) (string, bool, error) {
	now := time.Now()
	start := rand.IntN(set.limit)
	for try := range min(set.limit, maxSlotTries) {
		index := strconv.Itoa((start + try) % set.limit)
		_, err := s.db.PutItem(ctx, &dynamodb.PutItemInput{
			TableName: aws.String(s.table),
			Item: map[string]ddbtypes.AttributeValue{
				keyPK:         &ddbtypes.AttributeValueMemberS{Value: s.slotPartition(set.name)},
				keySK:         &ddbtypes.AttributeValueMemberS{Value: index},
				"holder":      &ddbtypes.AttributeValueMemberS{Value: holder},
				"lease_until": &ddbtypes.AttributeValueMemberN{Value: strconv.FormatInt(until.UnixMilli(), 10)},
				"expires_at":  &ddbtypes.AttributeValueMemberN{Value: strconv.FormatInt(until.Add(time.Hour).Unix(), 10)},
			},
			ConditionExpression: aws.String("attribute_not_exists(pk) OR lease_until < :now OR holder = :holder"),
			ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
				":now":    &ddbtypes.AttributeValueMemberN{Value: strconv.FormatInt(now.UnixMilli(), 10)},
				":holder": &ddbtypes.AttributeValueMemberS{Value: holder},
			},
		})
		if err == nil {
			return index, true, nil
		}
		if !isConditionFailed(err) {
			return "", false, fmt.Errorf("take a slot of %s: %w", set.name, err)
		}
	}
	return "", false, nil
}

func (s store) releaseSlot(ctx context.Context, set slotSet, index, holder string) error {
	_, err := s.db.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName:                 aws.String(s.table),
		Key:                       stringKey(s.slotPartition(set.name), index),
		ConditionExpression:       aws.String("holder = :holder"),
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{":holder": &ddbtypes.AttributeValueMemberS{Value: holder}},
	})
	if err != nil && !isConditionFailed(err) {
		return fmt.Errorf("release a slot of %s: %w", set.name, err)
	}
	return nil
}

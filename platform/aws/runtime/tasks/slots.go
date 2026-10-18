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

	"github.com/ocelhq/ocel/pkg/runs"
)

const (
	maxSlotTries   = 8
	slotPurgeDelay = time.Hour
)

type Slots struct {
	db     table
	table  string
	prefix string
}

func NewSlots(db table, tableName, prefix string) Slots {
	return Slots{db: db, table: tableName, prefix: prefix}
}

func (s Slots) partition(set string) string { return s.prefix + "slot#" + set }

func millisOf(at time.Time) *ddbtypes.AttributeValueMemberN {
	return &ddbtypes.AttributeValueMemberN{Value: strconv.FormatInt(at.UnixMilli(), 10)}
}

func unixOf(at time.Time) *ddbtypes.AttributeValueMemberN {
	return &ddbtypes.AttributeValueMemberN{Value: strconv.FormatInt(at.Unix(), 10)}
}

func (s Slots) Take(ctx context.Context, set runs.SlotSet, holder string, until time.Time) (runs.Slot, bool, error) {
	if set.Limit <= 0 {
		return runs.Slot{}, false, nil
	}
	start := rand.IntN(set.Limit)
	for try := range min(set.Limit, maxSlotTries) {
		index := strconv.Itoa((start + try) % set.Limit)
		item := stringKey(s.partition(set.Name), index)
		item["holder"] = &ddbtypes.AttributeValueMemberS{Value: holder}
		item["lease_until"] = millisOf(until)
		item["expires_at"] = unixOf(until.Add(slotPurgeDelay))
		_, err := s.db.PutItem(ctx, &dynamodb.PutItemInput{
			TableName:           aws.String(s.table),
			Item:                item,
			ConditionExpression: aws.String("attribute_not_exists(pk) OR lease_until < :now OR holder = :holder"),
			ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
				":now":    millisOf(time.Now()),
				":holder": &ddbtypes.AttributeValueMemberS{Value: holder},
			},
		})
		if err == nil {
			return runs.Slot{Set: set, Index: index, Holder: holder}, true, nil
		}
		if !isConditionFailed(err) {
			return runs.Slot{}, false, fmt.Errorf("take a slot of %s: %w", set.Name, err)
		}
	}
	return runs.Slot{}, false, nil
}

func (s Slots) Renew(ctx context.Context, slot runs.Slot, until time.Time) error {
	_, err := s.db.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:           aws.String(s.table),
		Key:                 stringKey(s.partition(slot.Set.Name), slot.Index),
		UpdateExpression:    aws.String("SET lease_until = :until, expires_at = :expires"),
		ConditionExpression: aws.String("holder = :holder"),
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":until":   millisOf(until),
			":expires": unixOf(until.Add(slotPurgeDelay)),
			":holder":  &ddbtypes.AttributeValueMemberS{Value: slot.Holder},
		},
	})
	if err != nil && !isConditionFailed(err) {
		return fmt.Errorf("renew a slot of %s: %w", slot.Set.Name, err)
	}
	return nil
}

func (s Slots) Release(ctx context.Context, slot runs.Slot) error {
	_, err := s.db.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName:                 aws.String(s.table),
		Key:                       stringKey(s.partition(slot.Set.Name), slot.Index),
		ConditionExpression:       aws.String("holder = :holder"),
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{":holder": &ddbtypes.AttributeValueMemberS{Value: slot.Holder}},
	})
	if err != nil && !isConditionFailed(err) {
		return fmt.Errorf("release a slot of %s: %w", slot.Set.Name, err)
	}
	return nil
}

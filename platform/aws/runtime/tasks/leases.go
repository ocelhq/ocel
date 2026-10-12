package tasks

import (
	"context"
	"log/slog"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/ocelhq/ocel/pkg/provider"
)

const polledLeaseTerm = hiddenFor + leaseMargin

type runLease struct {
	topic     string
	execution string
	delivery  string
}

type slotLease struct {
	set    slotSet
	index  string
	holder string
}

func (e *Engine) leaseUntil(ctx context.Context) time.Time {
	until := invocationDeadline(ctx).Add(leaseMargin)
	if renewed := time.Now().Add(polledLeaseTerm); e.pollsItsQueues && renewed.Before(until) {
		return renewed
	}
	return until
}

func (e *Engine) recordRunLease(deployed deployedConsumer, got arrival) {
	if !e.pollsItsQueues {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.runLeases[deployed.topicName+"#"+got.execution] = runLease{topic: deployed.topicName, execution: got.execution, delivery: got.msg.Delivery}
}

func (e *Engine) releaseLease(deployed deployedConsumer, execution string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.runLeases, deployed.topicName+"#"+execution)
}

func (e *Engine) recordSlotLease(slot takenSlot, holder string) {
	if !e.pollsItsQueues {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.slotLeases[slot.set.name+"#"+slot.index] = slotLease{set: slot.set, index: slot.index, holder: holder}
}

func (e *Engine) releaseSlotLease(slot takenSlot) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.slotLeases, slot.set.name+"#"+slot.index)
}

func (e *Engine) heldLeases() ([]runLease, []slotLease) {
	e.mu.Lock()
	defer e.mu.Unlock()
	runs := make([]runLease, 0, len(e.runLeases))
	for _, lease := range e.runLeases {
		runs = append(runs, lease)
	}
	slots := make([]slotLease, 0, len(e.slotLeases))
	for _, lease := range e.slotLeases {
		slots = append(slots, lease)
	}
	return runs, slots
}

func (c *Consumption) keepLeases(ctx context.Context) {
	ticker := time.NewTicker(c.renewEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		c.renewLeases(ctx)
	}
}

func (c *Consumption) renewLeases(ctx context.Context) {
	until := time.Now().Add(polledLeaseTerm)
	runs, slots := c.engine.heldLeases()
	for _, lease := range runs {
		if err := c.engine.store.renewRunLease(ctx, lease, until); err != nil {
			slog.Warn("renew the lease of a run being worked", "execution", lease.execution, "error", err)
		}
	}
	for _, lease := range slots {
		if err := c.engine.store.renewSlotLease(ctx, lease, until); err != nil {
			slog.Warn("renew the lease of a slot being worked", "set", lease.set.name, "error", err)
		}
	}
}

func (s store) renewRunLease(ctx context.Context, lease runLease, until time.Time) error {
	_, err := s.db.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:                aws.String(s.table),
		Key:                      s.runKey(lease.topic, lease.execution),
		UpdateExpression:         aws.String("SET lease_until = :until"),
		ConditionExpression:      aws.String("attribute_exists(pk) AND #status = :executing AND #delivery = :token"),
		ExpressionAttributeNames: map[string]string{"#status": "status", "#delivery": "delivery"},
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":until":     &ddbtypes.AttributeValueMemberN{Value: strconv.FormatInt(until.UnixMicro(), 10)},
			":executing": &ddbtypes.AttributeValueMemberS{Value: string(provider.RunExecuting)},
			":token":     &ddbtypes.AttributeValueMemberS{Value: lease.delivery},
		},
	})
	if isConditionFailed(err) {
		return nil
	}
	return err
}

func (s store) renewSlotLease(ctx context.Context, lease slotLease, until time.Time) error {
	_, err := s.db.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:           aws.String(s.table),
		Key:                 stringKey(s.slotPartition(lease.set.name), lease.index),
		UpdateExpression:    aws.String("SET lease_until = :until, expires_at = :expires"),
		ConditionExpression: aws.String("holder = :holder"),
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":until":   &ddbtypes.AttributeValueMemberN{Value: strconv.FormatInt(until.UnixMilli(), 10)},
			":expires": &ddbtypes.AttributeValueMemberN{Value: strconv.FormatInt(until.Add(time.Hour).Unix(), 10)},
			":holder":  &ddbtypes.AttributeValueMemberS{Value: lease.holder},
		},
	})
	if isConditionFailed(err) {
		return nil
	}
	return err
}

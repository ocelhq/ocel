package tasks

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/queues"
)

type leasingTable struct {
	table
	mu      sync.Mutex
	updates []*dynamodb.UpdateItemInput
	puts    []*dynamodb.PutItemInput
}

func (l *leasingTable) UpdateItem(_ context.Context, in *dynamodb.UpdateItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.updates = append(l.updates, in)
	return &dynamodb.UpdateItemOutput{Attributes: map[string]ddbtypes.AttributeValue{}}, nil
}

func (l *leasingTable) PutItem(_ context.Context, in *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.puts = append(l.puts, in)
	return &dynamodb.PutItemOutput{}, nil
}

func (l *leasingTable) DeleteItem(context.Context, *dynamodb.DeleteItemInput, ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error) {
	return &dynamodb.DeleteItemOutput{}, nil
}

func (l *leasingTable) renewals() []*dynamodb.UpdateItemInput {
	l.mu.Lock()
	defer l.mu.Unlock()
	var renewed []*dynamodb.UpdateItemInput
	for _, update := range l.updates {
		if strings.HasPrefix(aws.ToString(update.UpdateExpression), "SET lease_until") {
			renewed = append(renewed, update)
		}
	}
	return renewed
}

func leaseUntilOf(t *testing.T, values map[string]ddbtypes.AttributeValue, placeholder string) time.Time {
	t.Helper()
	number, ok := values[placeholder].(*ddbtypes.AttributeValueMemberN)
	if !ok {
		t.Fatalf("values %v carry no %s", values, placeholder)
	}
	n, err := strconv.ParseInt(number.Value, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return time.UnixMicro(n)
}

func setValueOf(t *testing.T, update *dynamodb.UpdateItemInput, attribute string) time.Time {
	t.Helper()
	for placeholder, named := range update.ExpressionAttributeNames {
		if named != attribute {
			continue
		}
		_, rest, found := strings.Cut(aws.ToString(update.UpdateExpression), placeholder+" = ")
		if !found {
			continue
		}
		value, _, _ := strings.Cut(rest, ",")
		return leaseUntilOf(t, update.ExpressionAttributeValues, strings.TrimSpace(value))
	}
	t.Fatalf("update %q sets no %s", aws.ToString(update.UpdateExpression), attribute)
	return time.Time{}
}

func leasingEngine(db *leasingTable) (*Engine, deployedConsumer) {
	consumer := provider.ConsumerSpec{Name: "resize", Worker: "media", Exclusive: true, Concurrency: 1, MaxDuration: 6 * time.Hour}
	declared := &provider.TopicSpec{Consumers: []provider.ConsumerSpec{consumer}}
	e := New(Config{Table: db, Worker: "media", Topology: queues.Topology{Table: "state", KeyPrefix: "p#", Topics: map[string]queues.Topic{"resize": {Declared: declared, Queues: map[string]string{"resize": "q"}}}}})
	return e, deployedConsumer{topicName: "resize", topic: declared, consumer: consumer, queue: "q"}
}

func TestARunAContainerWorkerClaimsIsLeasedForMinutesRatherThanItsWholeMaxDuration(t *testing.T) {
	t.Parallel()

	db := &leasingTable{}
	e, deployed := leasingEngine(db)
	NewConsumption(e, &scriptedQueue{})
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Hour)
	defer cancel()

	if _, claimed, err := e.claim(ctx, deployed, arrival{execution: "e-resize", msg: queueMessage{Delivery: "d"}}); err != nil || !claimed {
		t.Fatalf("claim() = %v, %v", claimed, err)
	}

	db.mu.Lock()
	claim := db.updates[0]
	db.mu.Unlock()
	if lease := time.Until(setValueOf(t, claim, "lease_until")); lease <= 0 || lease > 5*time.Minute {
		t.Errorf("the run is leased for %v, want a few minutes, so a task that crashes gives it up within a renewal or two rather than after six hours", lease)
	}
}

func TestAContainerWorkerRenewsTheLeasesOfTheRunsAndSlotsItHoldsAndNoOthers(t *testing.T) {
	t.Parallel()

	db := &leasingTable{}
	e, deployed := leasingEngine(db)
	c := NewConsumption(e, &scriptedQueue{})
	ctx := context.Background()
	if _, claimed, err := e.claim(ctx, deployed, arrival{execution: "e-resize", msg: queueMessage{Delivery: "d"}}); err != nil || !claimed {
		t.Fatalf("claim() = %v, %v", claimed, err)
	}
	slots, taken, err := e.takeSlots(ctx, deployed, "e-resize", e.leaseUntil(ctx))
	if err != nil || !taken {
		t.Fatalf("takeSlots() = %v, %v", taken, err)
	}

	c.renewLeases(ctx)

	renewed := db.renewals()
	if len(renewed) != 2 {
		t.Fatalf("renewed %d leases, want the run's and its slot's", len(renewed))
	}
	for _, renewal := range renewed {
		condition := aws.ToString(renewal.ConditionExpression)
		heldBySlot := strings.Contains(condition, "holder = :holder")
		heldByRun := strings.Contains(condition, "#status = :executing") && strings.Contains(condition, "#delivery = :token")
		if !heldBySlot && !heldByRun {
			t.Errorf("renewal condition %q, want it to touch only a lease this worker still holds", condition)
		}
		until := leaseUntilOf(t, renewal.ExpressionAttributeValues, ":until")
		if strings.Contains(condition, ":holder") {
			until = time.UnixMilli(until.UnixMicro())
		}
		if lease := time.Until(until); lease < time.Minute || lease > 5*time.Minute {
			t.Errorf("renewed for %v, want a few minutes from now", lease)
		}
		if strings.Contains(aws.ToString(renewal.UpdateExpression), "revision") {
			t.Errorf("renewal %q bumps the run's revision, so a write racing it would read as stale", aws.ToString(renewal.UpdateExpression))
		}
	}

	e.releaseLease(deployed, "e-resize")
	e.releaseSlots(ctx, slots, "e-resize")
	c.renewLeases(ctx)
	if again := db.renewals(); len(again) != 2 {
		t.Errorf("renewed %d leases after the worker let them go, want none more", len(again)-2)
	}
}

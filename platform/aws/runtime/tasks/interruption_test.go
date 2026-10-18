package tasks

import (
	"context"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/runs"
	"github.com/ocelhq/ocel/platform/aws/provider/queues"
)

type recordedUpdates struct {
	table
	updates []*dynamodb.UpdateItemInput
}

func (r *recordedUpdates) UpdateItem(_ context.Context, in *dynamodb.UpdateItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
	r.updates = append(r.updates, in)
	return &dynamodb.UpdateItemOutput{Attributes: map[string]ddbtypes.AttributeValue{}}, nil
}

func TestARunInterruptedMidAttemptIsQueuedAgainSoItsNextDeliveryRunsItWithoutWaitingOutItsLease(t *testing.T) {
	t.Parallel()

	db := &recordedUpdates{}
	consumer := provider.ConsumerSpec{Name: "resize", Worker: "media", Exclusive: true}
	declared := &provider.TopicSpec{Consumers: []provider.ConsumerSpec{consumer}}
	e := New(Config{Table: db, Topology: queues.Topology{Table: "state", KeyPrefix: "p#", Topics: map[string]queues.Topic{"resize": {Declared: declared, Queues: map[string]string{"resize": "q"}}}}})
	deployed := deployedConsumer{topicName: "resize", topic: declared, consumer: consumer, queue: "q"}

	next, _ := e.finish(context.Background(), deployed, arrival{execution: "e-resize", msg: queueMessage{Delivery: "d"}}, runs.Answer{Outcome: runs.Interrupted})

	if next != retained {
		t.Errorf("finish() = %v, want the message retained: the run did not finish", next)
	}
	if len(db.updates) != 1 {
		t.Fatalf("the run was updated %d times, want once to give back the lease the attempt held", len(db.updates))
	}
	update := db.updates[0]
	if !strings.Contains(*update.ConditionExpression, "#status = :executing") || !strings.Contains(*update.ConditionExpression, "#delivery = :token") {
		t.Errorf("condition %q, want it to touch only the run this delivery is executing", *update.ConditionExpression)
	}
	var queuedAgain bool
	for _, value := range update.ExpressionAttributeValues {
		if text, ok := value.(*ddbtypes.AttributeValueMemberS); ok && text.Value == string(provider.RunQueued) {
			queuedAgain = true
		}
	}
	if !queuedAgain {
		t.Errorf("update values %v name no queued status, so the next delivery would find the run still executing under a lease", update.ExpressionAttributeValues)
	}
}

package tasks

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/ocelhq/ocel/pkg/keyvalue"
	taskv1 "github.com/ocelhq/ocel/pkg/proto/app/task/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/taskstoretest"
	"github.com/ocelhq/ocel/platform/aws/provider/queues"
)

func TestTheDynamoDBStoreKeepsRunsAndRecordsAsEveryTaskStoreMust(t *testing.T) {
	taskstoretest.Run(t, func(t *testing.T) provider.TaskStore {
		em := live(t)
		return New(Config{Table: em.db, Manifest: queues.Manifest{
			Table:     em.table(t),
			KeyPrefix: "PROJECT#live#ENV#" + uniqueName(t) + "#TASKS#",
			Topics: map[string]queues.Topic{
				"resize":    {Declared: &provider.TopicSpec{}},
				"thumbnail": {Declared: &provider.TopicSpec{}},
			},
		}}).Store()
	})
}

func TestLiveARunTheEngineChangesIsReadByItsExecutionAtItsNewRevision(t *testing.T) {
	ctx := context.Background()
	em := live(t)
	d := em.deploy(t, map[string]*provider.TopicSpec{"resize": aTask("resize")}, nil)
	e := d.engine(nil)

	id := trigger(t, e, "resize", `{}`, &taskv1.TriggerOptions{DueAt: timestamppb.New(time.Now().Add(time.Hour))})
	delayed, err := e.Store().ReadRun(ctx, id)
	if err != nil || delayed.Status != provider.RunDelayed || delayed.Topic != "resize" {
		t.Fatalf("ReadRun of a triggered run = %+v, %v, want it delayed on resize", delayed, err)
	}
	if _, err := e.Tasks().CancelRun(ctx, &taskv1.CancelRunRequest{Id: id}); err != nil {
		t.Fatalf("CancelRun: %v", err)
	}
	canceled, err := e.Store().ReadRun(ctx, id)
	if err != nil || canceled.Status != provider.RunCanceled || canceled.Revision == delayed.Revision {
		t.Fatalf("ReadRun after a cancel = %s at %s, %v, want canceled at a revision past %s", canceled.Status, canceled.Revision, err, delayed.Revision)
	}
	if _, err := e.Store().WriteRun(ctx, delayed); !errors.Is(err, keyvalue.ErrStale) {
		t.Errorf("WriteRun at the revision before the cancel = %v, want ErrStale", err)
	}

	item, _, err := e.store.readRunItem(ctx, "resize", id, true)
	if err != nil {
		t.Fatal(err)
	}
	out, err := em.db.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(d.manifest.Table), Key: e.store.pointerKey(id), ConsistentRead: aws.Bool(true)})
	if err != nil {
		t.Fatal(err)
	}
	var pointer struct {
		Retention int64 `dynamodbav:"expires_at"`
	}
	if err := attributevalue.UnmarshalMap(out.Item, &pointer); err != nil || pointer.Retention != item.ExpiresAtUnix {
		t.Errorf("the run's pointer expires at %d, %v, want %d with the run it points to", pointer.Retention, err, item.ExpiresAtUnix)
	}
}

type racedTable struct {
	table
	race func()
}

func (r *racedTable) TransactWriteItems(ctx context.Context, in *dynamodb.TransactWriteItemsInput, optFns ...func(*dynamodb.Options)) (*dynamodb.TransactWriteItemsOutput, error) {
	out, err := r.table.TransactWriteItems(ctx, in, optFns...)
	if err == nil && r.race != nil {
		race := r.race
		r.race = nil
		race()
	}
	return out, err
}

func TestLiveARescheduleAnswersWithTheRunItWroteThoughALaterWriteLandsBeforeItReadsBack(t *testing.T) {
	ctx := context.Background()
	em := live(t)
	d := em.deploy(t, map[string]*provider.TopicSpec{"resize": aTask("resize")}, nil)
	e := d.engine(nil)

	id := trigger(t, e, "resize", `{}`, &taskv1.TriggerOptions{DueAt: timestamppb.New(time.Now().Add(time.Hour))})
	raced := &racedTable{table: e.store.db}
	raced.race = func() {
		if _, err := em.db.UpdateItem(ctx, &dynamodb.UpdateItemInput{
			TableName:                 aws.String(d.manifest.Table),
			Key:                       e.store.runKey("resize", id),
			UpdateExpression:          aws.String("SET #status = :canceled"),
			ExpressionAttributeNames:  map[string]string{"#status": "status"},
			ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{":canceled": &ddbtypes.AttributeValueMemberS{Value: string(provider.RunCanceled)}},
		}); err != nil {
			t.Errorf("the later write: %v", err)
		}
	}
	e.store.db = raced

	due := time.Now().Add(2 * time.Hour).Truncate(time.Microsecond)
	resp, err := e.Tasks().RescheduleRun(ctx, &taskv1.RescheduleRunRequest{Id: id, DueAt: timestamppb.New(due)})
	if err != nil {
		t.Fatalf("RescheduleRun: %v", err)
	}
	if run := resp.GetRun(); run.GetStatus() != taskv1.RunStatus_RUN_STATUS_DELAYED || !run.GetDueAt().AsTime().Equal(due) {
		t.Errorf("RescheduleRun answered %v, want the delayed run due at %v that it wrote, not the later write", run, due)
	}
}

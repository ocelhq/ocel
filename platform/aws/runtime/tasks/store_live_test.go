package tasks

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
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

	item, _, err := e.store.readRun(ctx, "resize", id, true)
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
	if err := attributevalue.UnmarshalMap(out.Item, &pointer); err != nil || pointer.Retention != item.Retention {
		t.Errorf("the run's pointer expires at %d, %v, want %d with the run it points to", pointer.Retention, err, item.Retention)
	}
}

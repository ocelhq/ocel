package taginvalidator_test

import (
	"bytes"
	"context"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/aws/provider/payloads"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/lambdapart/functiontest"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/taginvalidator"
)

const (
	testTable  = "ocel-bootstrap-StateTable-ABC"
	testStream = "arn:aws:dynamodb:us-east-1:" + functiontest.AccountID + ":table/" + testTable + "/stream/2026-01-01T00:00:00.000"
	testSeed   = "/ocel/edge/cloudflare/purge-seed"
)

type queues struct {
	mu      sync.Mutex
	created map[string]map[string]string
	tags    map[string]map[string]string
}

func (q *queues) CreateQueue(_ context.Context, in *sqs.CreateQueueInput, _ ...func(*sqs.Options)) (*sqs.CreateQueueOutput, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	name := aws.ToString(in.QueueName)
	if existing, ok := q.created[name]; ok && existing[string(sqstypes.QueueAttributeNameMessageRetentionPeriod)] != in.Attributes[string(sqstypes.QueueAttributeNameMessageRetentionPeriod)] {
		return nil, &sqstypes.QueueNameExists{Message: aws.String("exists with other attributes")}
	}
	q.created[name], q.tags[name] = in.Attributes, in.Tags
	return &sqs.CreateQueueOutput{QueueUrl: aws.String("https://sqs/" + name)}, nil
}

func (q *queues) SetQueueAttributes(_ context.Context, in *sqs.SetQueueAttributesInput, _ ...func(*sqs.Options)) (*sqs.SetQueueAttributesOutput, error) {
	return &sqs.SetQueueAttributesOutput{}, nil
}

func (q *queues) GetQueueUrl(_ context.Context, in *sqs.GetQueueUrlInput, _ ...func(*sqs.Options)) (*sqs.GetQueueUrlOutput, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if _, ok := q.created[aws.ToString(in.QueueName)]; !ok {
		return nil, &sqstypes.QueueDoesNotExist{Message: aws.String("no queue")}
	}
	return &sqs.GetQueueUrlOutput{QueueUrl: aws.String("https://sqs/" + aws.ToString(in.QueueName))}, nil
}

func (q *queues) DeleteQueue(_ context.Context, in *sqs.DeleteQueueInput, _ ...func(*sqs.Options)) (*sqs.DeleteQueueOutput, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	name := aws.ToString(in.QueueUrl)[len("https://sqs/"):]
	delete(q.created, name)
	return &sqs.DeleteQueueOutput{}, nil
}

func clients(account *functiontest.Account, q *queues) taginvalidator.Clients {
	return taginvalidator.Clients{Lambda: account.Lambda(), Logs: account.Clients().Logs, SQS: q}
}

func newQueues() *queues {
	return &queues{created: map[string]map[string]string{}, tags: map[string]map[string]string{}}
}

func spec(tier environment.Tier) taginvalidator.Spec {
	return taginvalidator.Spec{
		Namespace:  "ocel",
		Tier:       tier,
		Region:     functiontest.Region,
		Account:    functiontest.AccountID,
		Role:       functiontest.RoleARN("/ocel/"+string(tier)+"/bootstrap/", taginvalidator.NamesFor("ocel", tier).Role),
		StateTable: testTable,
		StreamARN:  testStream,
		SeedParam:  testSeed,
	}
}

func TestEnsureMakesAnInvalidatorTriggeredByTheStateTablesTagWrites(t *testing.T) {
	t.Parallel()
	account, q := functiontest.New(), newQueues()
	names := taginvalidator.NamesFor("ocel", environment.TierProduction)

	if _, err := taginvalidator.Ensure(context.Background(), clients(account, q), spec(environment.TierProduction)); err != nil {
		t.Fatalf("Ensure() = %v", err)
	}

	fn, ok := account.Functions[names.Function]
	if !ok {
		t.Fatalf("no function %s", names.Function)
	}
	if !bytes.Equal(fn.Code, payloads.TagInvalidator().Bytes) || aws.ToString(fn.Input.Role) != spec(environment.TierProduction).Role {
		t.Errorf("function = %+v, want the tag invalidator payload running as the base's invalidator role", fn.Input)
	}
	if fn.Input.Environment.Variables["OCEL_STATE_TABLE"] != testTable || fn.Input.Environment.Variables["OCEL_PURGE_SEED_PARAM"] != testSeed {
		t.Errorf("function environment = %v, want the state table and the purge seed parameter", fn.Input.Environment.Variables)
	}
	if fn.Tags["ocel:managed-by"] != "ocel" || fn.Tags["ocel:env-tier"] != "production" {
		t.Errorf("function tags = %v, want ocel's of production, the tags the deploy credential creates under", fn.Tags)
	}
	if _, ok := q.created[names.Queue]; !ok {
		t.Errorf("queues = %v, want the dead letter queue %s", q.created, names.Queue)
	}
	if len(account.Mappings) != 1 {
		t.Fatalf("mappings = %v, want one on the state table's stream", account.Mappings)
	}
	for _, mapping := range account.Mappings {
		if aws.ToString(mapping.Input.EventSourceArn) != testStream || aws.ToString(mapping.Input.DestinationConfig.OnFailure.Destination) != "arn:aws:sqs:"+functiontest.Region+":"+functiontest.AccountID+":"+names.Queue {
			t.Errorf("mapping = %+v, want it on the stream, failing over to the dead letter queue", mapping.Input)
		}
		if len(mapping.Input.FilterCriteria.Filters) != 1 {
			t.Errorf("mapping filters = %v, want the one filter to tag records", mapping.Input.FilterCriteria)
		}
	}
}

func TestEnsureWaitsOutARoleWhoseStreamPermissionLambdaCannotSeeYet(t *testing.T) {
	t.Parallel()
	account, q := functiontest.New(), newQueues()
	account.Unpermitted = 1

	if _, err := taginvalidator.Ensure(context.Background(), clients(account, q), spec(environment.TierProduction)); err != nil {
		t.Fatalf("Ensure() while the role's policy propagates = %v, want it waited out", err)
	}
	if len(account.Mappings) != 1 {
		t.Errorf("mappings = %v, want one on the state table's stream once the role's policy is visible", account.Mappings)
	}
}

func TestEnsureAgainUpdatesTheOneMappingItMade(t *testing.T) {
	t.Parallel()
	account, q := functiontest.New(), newQueues()
	for range 2 {
		if _, err := taginvalidator.Ensure(context.Background(), clients(account, q), spec(environment.TierPreview)); err != nil {
			t.Fatalf("Ensure() = %v", err)
		}
	}

	if len(account.Mappings) != 1 || account.Called("UpdateEventSourceMapping") != 1 {
		t.Errorf("mappings %d, updates %d; want one mapping, updated by the second run", len(account.Mappings), account.Called("UpdateEventSourceMapping"))
	}
}

func TestNamesKeepTheInvalidatorAndItsQueueOutOfTheAppFence(t *testing.T) {
	t.Parallel()
	names := taginvalidator.NamesFor("ocel", environment.TierPreview)

	if names.Queue != "ocel-preview-invalidator" || names.Function != "ocel-preview-invalidator" {
		t.Errorf("NamesFor() = %+v, want the preview invalidator and its queue named ocel-preview-invalidator, outside the ocel-app- fence of app resources", names)
	}
}

func TestRemoveTakesTheInvalidatorItsMappingAndItsQueueDown(t *testing.T) {
	t.Parallel()
	account, q := functiontest.New(), newQueues()
	if _, err := taginvalidator.Ensure(context.Background(), clients(account, q), spec(environment.TierProduction)); err != nil {
		t.Fatalf("Ensure() = %v", err)
	}

	for range 2 {
		if err := taginvalidator.Remove(context.Background(), clients(account, q), "ocel", environment.TierProduction); err != nil {
			t.Fatalf("Remove() = %v", err)
		}
	}

	if len(account.Functions) != 0 || len(account.Mappings) != 0 || len(account.LogGroups) != 0 || len(q.created) != 0 {
		t.Errorf("left functions %v mappings %v log groups %v queues %v, want none", account.Functions, account.Mappings, account.LogGroups, q.created)
	}
}

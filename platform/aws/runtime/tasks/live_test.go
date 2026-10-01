package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/envelope"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/runs"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"

	topicv1 "github.com/ocelhq/ocel/pkg/proto/app/topic/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/queues"
)

const liveRegion = "us-east-1"

type emulator struct {
	aws aws.Config
	db  *dynamodb.Client
	sqs *sqs.Client
	sns *sns.Client
}

func live(t *testing.T) emulator {
	t.Helper()
	endpoint := os.Getenv("OCEL_FLOCI_ENDPOINT")
	if endpoint == "" {
		t.Skip("no floci emulator in the environment; run under `scripts/floci.sh run <name> -- go test ./...`")
	}
	cfg := aws.Config{
		Region:       liveRegion,
		Credentials:  credentials.NewStaticCredentialsProvider("test", "test", ""),
		BaseEndpoint: aws.String(endpoint),
	}
	return emulator{aws: cfg, db: dynamodb.NewFromConfig(cfg), sqs: sqs.NewFromConfig(cfg), sns: sns.NewFromConfig(cfg)}
}

func uniqueName(t *testing.T) string {
	sum := fmt.Sprintf("%x", time.Now().UnixNano())
	name := strings.NewReplacer("/", "-", "_", "-").Replace(strings.ToLower(t.Name()))
	if len(name) > 40 {
		name = name[len(name)-40:]
	}
	return strings.Trim(name, "-") + "-" + sum[len(sum)-8:]
}

func (em emulator) table(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	name := uniqueName(t)
	if _, err := em.db.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName:   aws.String(name),
		BillingMode: ddbtypes.BillingModePayPerRequest,
		AttributeDefinitions: []ddbtypes.AttributeDefinition{
			{AttributeName: aws.String(keyPK), AttributeType: ddbtypes.ScalarAttributeTypeS},
			{AttributeName: aws.String(keySK), AttributeType: ddbtypes.ScalarAttributeTypeS},
		},
		KeySchema: []ddbtypes.KeySchemaElement{
			{AttributeName: aws.String(keyPK), KeyType: ddbtypes.KeyTypeHash},
			{AttributeName: aws.String(keySK), KeyType: ddbtypes.KeyTypeRange},
		},
	}); err != nil {
		t.Fatalf("create table: %v", err)
	}
	t.Cleanup(func() {
		_, _ = em.db.DeleteTable(context.Background(), &dynamodb.DeleteTableInput{TableName: aws.String(name)})
	})
	return name
}

func (em emulator) queue(t *testing.T, name string) string {
	t.Helper()
	attributes := map[string]string{"VisibilityTimeout": "30"}
	if queues.IsFIFO(name) {
		attributes["FifoQueue"] = "true"
		attributes["ContentBasedDeduplication"] = "true"
	}
	out, err := em.sqs.CreateQueue(context.Background(), &sqs.CreateQueueInput{QueueName: aws.String(name), Attributes: attributes})
	if err != nil {
		t.Fatalf("create queue %s: %v", name, err)
	}
	t.Cleanup(func() { _, _ = em.sqs.DeleteQueue(context.Background(), &sqs.DeleteQueueInput{QueueUrl: out.QueueUrl}) })
	return *out.QueueUrl
}

func (em emulator) queueARN(t *testing.T, url string) string {
	t.Helper()
	out, err := em.sqs.GetQueueAttributes(context.Background(), &sqs.GetQueueAttributesInput{QueueUrl: aws.String(url), AttributeNames: []sqstypes.QueueAttributeName{sqstypes.QueueAttributeNameQueueArn}})
	if err != nil {
		t.Fatalf("read queue %s: %v", url, err)
	}
	return out.Attributes[string(sqstypes.QueueAttributeNameQueueArn)]
}

type deployment struct {
	em       emulator
	manifest queues.Manifest
	urls     map[string]string
	arns     map[string]string
}

func (em emulator) deploy(t *testing.T, topics map[string]*provider.TopicSpec, workers map[string]queues.Worker) *deployment {
	t.Helper()
	ctx := context.Background()
	d := &deployment{em: em, urls: map[string]string{}, arns: map[string]string{}, manifest: queues.Manifest{
		Table:     em.table(t),
		KeyPrefix: "PROJECT#live#ENV#" + uniqueName(t) + "#TASKS#",
		Topics:    map[string]queues.Topic{},
		Workers:   workers,
	}}
	suffix := uniqueName(t)
	for name, declared := range topics {
		topic := queues.Topic{Declared: declared, Queues: map[string]string{}}
		if !runs.IsTask(declared) {
			topicName := "t-" + name + "-" + suffix
			if declared.Ordered {
				topicName += ".fifo"
			}
			attributes := map[string]string{}
			if declared.Ordered {
				attributes["FifoTopic"] = "true"
			}
			out, err := em.sns.CreateTopic(ctx, &sns.CreateTopicInput{Name: aws.String(topicName), Attributes: attributes})
			if err != nil {
				t.Fatalf("create topic %s: %v", topicName, err)
			}
			topic.SNS = *out.TopicArn
			t.Cleanup(func() { _, _ = em.sns.DeleteTopic(context.Background(), &sns.DeleteTopicInput{TopicArn: out.TopicArn}) })
		}
		for _, consumer := range declared.Consumers {
			queue := "q-" + name + "-" + consumer.Name + "-" + suffix
			if declared.Ordered {
				queue += ".fifo"
			}
			url := em.queue(t, queue)
			topic.Queues[consumer.Name] = queue
			d.urls[queue] = url
			d.arns[queue] = em.queueARN(t, url)
			if topic.SNS != "" {
				if _, err := em.sns.Subscribe(ctx, &sns.SubscribeInput{
					TopicArn:   aws.String(topic.SNS),
					Protocol:   aws.String("sqs"),
					Endpoint:   aws.String(d.arns[queue]),
					Attributes: map[string]string{"RawMessageDelivery": "true"},
				}); err != nil {
					t.Fatalf("subscribe %s to %s: %v", queue, topic.SNS, err)
				}
			}
		}
		d.manifest.Topics[name] = topic
	}
	return d
}

func (d *deployment) engine(worker *fakeWorker) *Engine {
	cfg := Config{Manifest: d.manifest, Table: d.em.db, Queues: d.em.sqs, Topics: d.em.sns}
	if worker != nil {
		cfg.WorkerURL = worker.server.URL
	}
	return New(cfg)
}

func (d *deployment) queueOf(topic, consumer string) string {
	return d.manifest.Topics[topic].Queues[consumer]
}

func (d *deployment) receive(t *testing.T, topic, consumer string, within time.Duration) events.SQSMessage {
	t.Helper()
	queue := d.queueOf(topic, consumer)
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		out, err := d.em.sqs.ReceiveMessage(context.Background(), &sqs.ReceiveMessageInput{
			QueueUrl:            aws.String(d.urls[queue]),
			MaxNumberOfMessages: 1,
			WaitTimeSeconds:     1,
			VisibilityTimeout:   30,
		})
		if err != nil {
			t.Fatalf("receive from %s: %v", queue, err)
		}
		if len(out.Messages) == 1 {
			msg := out.Messages[0]
			return events.SQSMessage{MessageId: *msg.MessageId, ReceiptHandle: *msg.ReceiptHandle, Body: *msg.Body, EventSourceARN: d.arns[queue], EventSource: "aws:sqs"}
		}
	}
	t.Fatalf("no message reached %s within %v", queue, within)
	return events.SQSMessage{}
}

func (d *deployment) quiet(t *testing.T, topic, consumer string, within time.Duration) {
	t.Helper()
	queue := d.queueOf(topic, consumer)
	out, err := d.em.sqs.ReceiveMessage(context.Background(), &sqs.ReceiveMessageInput{
		QueueUrl:            aws.String(d.urls[queue]),
		MaxNumberOfMessages: 1,
		WaitTimeSeconds:     int32(within / time.Second),
	})
	if err != nil {
		t.Fatalf("receive from %s: %v", queue, err)
	}
	if len(out.Messages) > 0 {
		t.Fatalf("%s holds %s, want it empty", queue, *out.Messages[0].Body)
	}
}

func (d *deployment) acknowledge(t *testing.T, topic, consumer string, record events.SQSMessage) {
	t.Helper()
	if _, err := d.em.sqs.DeleteMessage(context.Background(), &sqs.DeleteMessageInput{QueueUrl: aws.String(d.urls[d.queueOf(topic, consumer)]), ReceiptHandle: aws.String(record.ReceiptHandle)}); err != nil {
		t.Fatalf("delete a delivered message: %v", err)
	}
}

func deliver(t *testing.T, d *deployment, e *Engine, topic, consumer string, records ...events.SQSMessage) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	resp := e.Deliver(ctx, events.SQSEvent{Records: records})
	failed := map[string]bool{}
	var ids []string
	for _, failure := range resp.BatchItemFailures {
		failed[failure.ItemIdentifier] = true
		ids = append(ids, failure.ItemIdentifier)
	}
	for _, record := range records {
		if !failed[record.MessageId] {
			d.acknowledge(t, topic, consumer, record)
		}
	}
	return ids
}

type answer struct {
	status int
	body   string
	hold   time.Duration
}

type delivered struct {
	envelope *topicv1.Envelope
	body     []byte
}

type fakeWorker struct {
	server  *httptest.Server
	respond func(*topicv1.Envelope) answer

	mu       sync.Mutex
	received []delivered
	aborted  int
}

func newFakeWorker(t *testing.T, respond func(*topicv1.Envelope) answer) *fakeWorker {
	t.Helper()
	w := &fakeWorker{respond: respond}
	w.server = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		envelope, err := envelope.Decode(body)
		if err != nil {
			http.Error(rw, err.Error(), http.StatusBadRequest)
			return
		}
		w.mu.Lock()
		w.received = append(w.received, delivered{envelope: envelope, body: body})
		w.mu.Unlock()
		reply := w.respond(envelope)
		if reply.hold > 0 {
			select {
			case <-req.Context().Done():
				w.mu.Lock()
				w.aborted++
				w.mu.Unlock()
				return
			case <-time.After(reply.hold):
			}
		}
		rw.WriteHeader(reply.status)
		_, _ = rw.Write([]byte(reply.body))
	}))
	t.Cleanup(w.server.Close)
	return w
}

func (w *fakeWorker) deliveries() []delivered {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]delivered(nil), w.received...)
}

func succeeding(*topicv1.Envelope) answer {
	return answer{status: http.StatusOK, body: `{"done":true}`}
}

func rawField(t *testing.T, body []byte, name string) string {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatalf("%s is not a JSON object: %v", body, err)
	}
	return string(fields[name])
}

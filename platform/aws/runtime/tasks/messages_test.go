package tasks

import (
	"context"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/provider"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

type sentMessages struct {
	sent []*sqs.SendMessageInput
}

func (s *sentMessages) GetQueueUrl(_ context.Context, in *sqs.GetQueueUrlInput, _ ...func(*sqs.Options)) (*sqs.GetQueueUrlOutput, error) {
	return &sqs.GetQueueUrlOutput{QueueUrl: aws.String("https://sqs.test/" + *in.QueueName)}, nil
}

func (s *sentMessages) SendMessage(_ context.Context, in *sqs.SendMessageInput, _ ...func(*sqs.Options)) (*sqs.SendMessageOutput, error) {
	s.sent = append(s.sent, in)
	return &sqs.SendMessageOutput{}, nil
}

func (s *sentMessages) ChangeMessageVisibility(context.Context, *sqs.ChangeMessageVisibilityInput, ...func(*sqs.Options)) (*sqs.ChangeMessageVisibilityOutput, error) {
	return &sqs.ChangeMessageVisibilityOutput{}, nil
}

func TestADelayedMessageToAFIFOQueueCarriesNoPerMessageDelayWhichSQSRefusesThough(t *testing.T) {
	t.Parallel()

	queue := &sentMessages{}
	e := New(Config{Queues: queue})
	consumer := provider.ConsumerSpec{Name: "sequence", Exclusive: true}
	for name, tc := range map[string]struct {
		queue     string
		delay     time.Duration
		wantDelay int32
		wantGroup string
	}{
		"fifo, keyed":       {queue: "q.fifo", delay: 10 * time.Minute, wantGroup: "k"},
		"standard, short":   {queue: "q", delay: 3 * time.Second, wantDelay: 3},
		"standard, past 15": {queue: "q", delay: 2 * time.Hour, wantDelay: 900},
	} {
		queue.sent = nil
		deployed := deployedConsumer{topicName: "sequence", topic: &provider.TopicSpec{Consumers: []provider.ConsumerSpec{consumer}}, consumer: consumer, queue: tc.queue}
		if err := e.enqueue(context.Background(), deployed, queueMessage{Execution: "e-sequence", Delivery: "d", Key: "k"}, tc.delay); err != nil {
			t.Fatalf("%s: enqueue: %v", name, err)
		}
		in := queue.sent[0]
		if in.DelaySeconds != tc.wantDelay || aws.ToString(in.MessageGroupId) != tc.wantGroup {
			t.Errorf("%s: sent with delay %ds and group %q, want delay %ds and group %q", name, in.DelaySeconds, aws.ToString(in.MessageGroupId), tc.wantDelay, tc.wantGroup)
		}
		if (aws.ToString(in.MessageDeduplicationId) != "") != (tc.wantGroup != "") {
			t.Errorf("%s: deduplication id %q, want one exactly on a FIFO queue", name, aws.ToString(in.MessageDeduplicationId))
		}
	}
}

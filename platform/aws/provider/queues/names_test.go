package queues

import (
	"strings"
	"testing"
)

func TestAQueueNameFitsSQSAndEndsInFIFOOnlyForAnOrderedConsumer(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("a", 63)
	for _, fifo := range []bool{false, true} {
		name := QueueName("shop", "production", long, long, fifo)
		if len(name) > 80 {
			t.Errorf("QueueName(fifo %v) = %q, %d characters, want at most SQS's 80", fifo, name, len(name))
		}
		if IsFIFO(name) != fifo || IsFIFO(DeadLetterQueueName("shop", "production", long, long, fifo)) != fifo {
			t.Errorf("QueueName(fifo %v) = %q, want the .fifo suffix exactly when ordered", fifo, name)
		}
		if !strings.HasPrefix(name, "ocel-app-shop-production-") {
			t.Errorf("QueueName = %q, want it under the app scope, project and environment", name)
		}
	}
}

func TestTwoConsumersWhoseNamesJoinTheSameWayGetTheirOwnQueues(t *testing.T) {
	t.Parallel()

	if QueueName("shop", "prod", "a-b", "c", false) == QueueName("shop", "prod", "a", "b-c", false) {
		t.Error("topic a-b's consumer c and topic a's consumer b-c share a queue name")
	}
	if QueueName("shop", "prod", "orders", "audit", false) == DeadLetterQueueName("shop", "prod", "orders", "audit", false) {
		t.Error("a consumer's queue and its dead-letter queue share a name")
	}
}

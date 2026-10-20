package queues

import (
	"strings"
	"testing"
)

var shop = Names{Namespace: "ocel", Project: "acme", Env: "prod"}

func TestAQueueIsNamedForItsTopicAndConsumerUnderTheAppFenceWithItsDeadLetterQueueBesideIt(t *testing.T) {
	t.Parallel()

	got, err := shop.Queues("orders", "ship", false)
	if err != nil {
		t.Fatal(err)
	}
	want := QueueNames{Queue: "ocel-app-acme-prod-orders-ship-b31177", DeadLetter: "ocel-app-acme-prod-orders-ship-b31177-dlq"}
	if got != want {
		t.Errorf("Queues = %+v, want %+v", got, want)
	}
}

func TestAnOrderedConsumersQueuesEndInFIFOAndFitSQS(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("a", 20)
	for _, fifo := range []bool{false, true} {
		got, err := Names{Namespace: "ocel", Project: strings.Repeat("p", 30), Env: strings.Repeat("e", 30)}.Queues(long, long, fifo)
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{got.Queue, got.DeadLetter} {
			if len(name) > maxQueueNameLen {
				t.Errorf("fifo %v: %q is %d characters, want at most SQS's %d", fifo, name, len(name), maxQueueNameLen)
			}
			if IsFIFO(name) != fifo {
				t.Errorf("fifo %v: %q, want the .fifo suffix exactly when ordered", fifo, name)
			}
		}
		if strings.TrimSuffix(got.DeadLetter, fifoSuffix) != strings.TrimSuffix(got.Queue, fifoSuffix)+"-dlq" {
			t.Errorf("fifo %v: dead-letter queue %q is not its queue %q plus -dlq", fifo, got.DeadLetter, got.Queue)
		}
	}
}

func TestTwoConsumersWhoseNamesJoinTheSameWayGetTheirOwnQueues(t *testing.T) {
	t.Parallel()

	one, err := shop.Queues("a-b", "c", false)
	if err != nil {
		t.Fatal(err)
	}
	two, err := shop.Queues("a", "b-c", false)
	if err != nil {
		t.Fatal(err)
	}
	if one.Queue == two.Queue {
		t.Errorf("topic a-b's consumer c and topic a's consumer b-c share the queue %q", one.Queue)
	}
}

func TestAQueueWhoseDeclaredNamesCannotFitIsRefusedNamingTheConsumerAndTheLimit(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("a", 40)
	_, err := shop.Queues(long, long, false)
	if err == nil {
		t.Fatal("a queue whose topic and consumer alone overrun SQS's limit was named rather than refused")
	}
	if !strings.Contains(err.Error(), long) || !strings.Contains(err.Error(), "80") {
		t.Errorf("refusal %q, want it to name the consumer and SQS's 80 characters", err)
	}
}

func TestATopicIsNamedUnderTheAppFence(t *testing.T) {
	t.Parallel()

	got, err := shop.Topic("orders", true)
	if err != nil {
		t.Fatal(err)
	}
	if want := "ocel-app-acme-prod-orders-ac8eed.fifo"; got != want {
		t.Errorf("Topic = %q, want %q", got, want)
	}
}

func TestAnEnvsCronSchedulesSitInOneGroupUnderTheAppFence(t *testing.T) {
	t.Parallel()

	group, err := shop.ScheduleGroup()
	if err != nil {
		t.Fatal(err)
	}
	if want := "ocel-app-acme-prod-cron-96cfc3"; group != want {
		t.Errorf("ScheduleGroup = %q, want %q", group, want)
	}
	one, err := shop.Schedules("nightly", 1)
	if err != nil {
		t.Fatal(err)
	}
	two, err := shop.Schedules("nightly", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(one) != 1 || len(two) != 2 || two[0] != one[0]+"-1" || two[1] != one[0]+"-2" {
		t.Errorf("Schedules = %v and %v, want one schedule, or one per expression numbered after it", one, two)
	}
}

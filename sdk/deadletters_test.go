package ocel_test

import (
	"slices"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
	"ocel.dev"
	topicv1 "ocel.dev/internal/proto/app/topic/v1"
)

func TestDeadLetterListReadsAPageOfTheConsumersDeadLetters(t *testing.T) {
	runtime := serveRuntime(t, map[string]string{"OCEL_RESOURCE_TOPIC_orders": deliveredTopic})
	failedAt := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	runtime.letters = []*topicv1.DeadLetter{{
		Execution: "01HZY4B6Q0Z0Z0Z0Z0Z0Z0Z0Z0-audit",
		Message:   &topicv1.Message{Id: "01HZY4B6Q0Z0Z0Z0Z0Z0Z0Z0Z0", PublishedAt: timestamppb.New(failedAt.Add(-time.Hour))},
		Payload:   structpb.NewStructValue(&structpb.Struct{Fields: map[string]*structpb.Value{"id": structpb.NewStringValue("o-1")}}),
		Attempts:  3,
		Error:     "the ledger refused it",
		FailedAt:  timestamppb.New(failedAt),
	}}

	page, err := ocel.Topic[order]("orders").DeadLetter("audit").List(t.Context(), ocel.Cursor("c-1"), ocel.Limit(20))
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}

	req := only[*topicv1.ListDeadLettersRequest](t, runtime)
	if req.GetTopic() != "orders" || req.GetConsumer() != "audit" || req.GetCursor() != "c-1" || req.GetLimit() != 20 {
		t.Errorf("request = %v", req)
	}
	if page.NextCursor != "after-letters" || len(page.DeadLetters) != 1 {
		t.Fatalf("page = %+v", page)
	}
	letter := page.DeadLetters[0]
	if letter.Execution != "01HZY4B6Q0Z0Z0Z0Z0Z0Z0Z0Z0-audit" || letter.Message.ID != "01HZY4B6Q0Z0Z0Z0Z0Z0Z0Z0Z0" ||
		string(letter.Payload) != `{"id":"o-1"}` || letter.Attempts != 3 || letter.Error != "the ledger refused it" ||
		!letter.FailedAt.Equal(failedAt) || !letter.Message.PublishedAt.Equal(failedAt.Add(-time.Hour)) {
		t.Errorf("dead letter = %+v", letter)
	}
}

func TestDeadLetterRedriveSendsTheNamedExecutionsBack(t *testing.T) {
	runtime := serveRuntime(t, map[string]string{"OCEL_RESOURCE_TOPIC_orders": deliveredTopic})

	redriven, err := ocel.Topic[order]("orders").DeadLetter("audit").Redrive(t.Context(), "e-1", "e-2")
	if err != nil {
		t.Fatalf("Redrive() error = %v", err)
	}

	req := only[*topicv1.RedriveDeadLettersRequest](t, runtime)
	if redriven != 3 || req.GetTopic() != "orders" || req.GetConsumer() != "audit" ||
		!slices.Equal(req.GetExecutions(), []string{"e-1", "e-2"}) {
		t.Errorf("Redrive() = %d after %v", redriven, req)
	}
}

func TestDeadLetterPurgeWithNoExecutionsPurgesEveryOne(t *testing.T) {
	runtime := serveRuntime(t, map[string]string{"OCEL_RESOURCE_TOPIC_orders": deliveredTopic})

	purged, err := ocel.Topic[order]("orders").DeadLetter("audit").Purge(t.Context())
	if err != nil {
		t.Fatalf("Purge() error = %v", err)
	}

	req := only[*topicv1.PurgeDeadLettersRequest](t, runtime)
	if purged != 2 || req.GetConsumer() != "audit" || len(req.GetExecutions()) != 0 {
		t.Errorf("Purge() = %d after %v", purged, req)
	}
}

func TestDeadLetterCountAsksTheRuntimeForTheConsumersCount(t *testing.T) {
	runtime := serveRuntime(t, map[string]string{"OCEL_RESOURCE_TOPIC_orders": deliveredTopic})

	count, err := ocel.Topic[order]("orders").DeadLetter("audit").Count(t.Context())
	if err != nil {
		t.Fatalf("Count() error = %v", err)
	}

	req := only[*topicv1.CountDeadLettersRequest](t, runtime)
	if count != 7 || req.GetTopic() != "orders" || req.GetConsumer() != "audit" {
		t.Errorf("Count() = %d after %v", count, req)
	}
}

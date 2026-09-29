package providerserver

import (
	"context"
	"errors"
	"testing"

	connect "connectrpc.com/connect"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

func posedQuestionID(t *testing.T, q *questions, confirmed *int) string {
	t.Helper()
	asked := provider.Ask("confirm this", provider.Question{
		Finding: "a finding",
		Prompt:  "go ahead?",
		Confirm: func(context.Context) error { *confirmed++; return nil },
	})
	err := q.pose(provider.RefusalError(asked))
	var rpcErr *connect.Error
	if !errors.As(err, &rpcErr) {
		t.Fatalf("pose() = %T, want a connect error", err)
	}
	for _, detail := range rpcErr.Details() {
		value, err := detail.Value()
		if err != nil {
			t.Fatal(err)
		}
		if question, ok := value.(*contractv1.Question); ok {
			return question.GetId()
		}
	}
	t.Fatal("pose() attached no question")
	return ""
}

func TestQuestionsKeepOnlyTheMostRecentOnesUnanswered(t *testing.T) {
	q := newQuestions()
	confirmed := 0
	first := posedQuestionID(t, q, &confirmed)
	var latest string
	for range maxPendingQuestions {
		latest = posedQuestionID(t, q, &confirmed)
	}

	if len(q.pending) != maxPendingQuestions {
		t.Errorf("%d questions pending, want at most %d", len(q.pending), maxPendingQuestions)
	}
	if err := q.confirm(context.Background(), first); err == nil {
		t.Error("confirming the oldest unanswered question succeeded, want it forgotten once newer ones filled the pending set")
	}
	if err := q.confirm(context.Background(), latest); err != nil || confirmed != 1 {
		t.Errorf("confirming the latest question = %v, confirmed %d times, want it answered once", err, confirmed)
	}
}

package providerserver

import (
	"context"
	"errors"
	"strings"
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
		Remedy:  "ocel bootstrap production",
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

func TestAQuestionPosedOverTheWireKeepsItsRemedy(t *testing.T) {
	q := newQuestions()
	const remedy = "ssh-keyscan -t ssh-ed25519 203.0.113.10 >> ~/.ssh/known_hosts"
	asked := provider.Ask("confirm this", provider.Question{
		Finding: "a finding",
		Prompt:  "go ahead?",
		Remedy:  remedy,
		Confirm: func(context.Context) error { return nil },
	})

	var rpcErr *connect.Error
	if !errors.As(q.pose(provider.RefusalError(asked)), &rpcErr) {
		t.Fatal("pose() did not return a connect error")
	}
	for _, detail := range rpcErr.Details() {
		value, err := detail.Value()
		if err != nil {
			t.Fatal(err)
		}
		if question, ok := value.(*contractv1.Question); ok {
			if question.GetRemedy() != remedy {
				t.Errorf("remedy = %q, want %q", question.GetRemedy(), remedy)
			}
			return
		}
	}
	t.Fatal("pose() attached no question")
}

func TestAQuestionWithoutARemedyIsRefusedRatherThanPosed(t *testing.T) {
	q := newQuestions()
	asked := provider.Ask("confirm this", provider.Question{
		Finding: "a finding",
		Prompt:  "go ahead?",
		Confirm: func(context.Context) error { return nil },
	})

	err := q.pose(provider.RefusalError(asked))
	var rpcErr *connect.Error
	if !errors.As(err, &rpcErr) {
		t.Fatalf("pose() = %T, want a connect error", err)
	}
	if rpcErr.Code() != connect.CodeInternal {
		t.Errorf("code = %v, want %v: a question with nothing to type where nobody can answer is a provider bug", rpcErr.Code(), connect.CodeInternal)
	}
	if !strings.Contains(rpcErr.Message(), "remedy") || !strings.Contains(rpcErr.Message(), "go ahead?") {
		t.Errorf("message = %q, want the question named as carrying no remedy", rpcErr.Message())
	}
	for _, detail := range rpcErr.Details() {
		if value, _ := detail.Value(); value != nil {
			if _, ok := value.(*contractv1.Question); ok {
				t.Error("pose() attached the question, want it withheld until it carries a remedy")
			}
		}
	}
	if len(q.pending) != 0 {
		t.Errorf("%d questions pending, want none", len(q.pending))
	}
}

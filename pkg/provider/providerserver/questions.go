package providerserver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"

	connect "connectrpc.com/connect"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

type questions struct {
	mu      sync.Mutex
	pending map[string]provider.Question
}

func newQuestions() *questions {
	return &questions{pending: map[string]provider.Question{}}
}

func (q *questions) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		res, err := next(ctx, req)
		return res, q.pose(err)
	}
}

func (q *questions) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (q *questions) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		return q.pose(next(ctx, conn))
	}
}

func (q *questions) pose(err error) error {
	question, asked := provider.QuestionOf(err)
	var rpcErr *connect.Error
	if !asked || !errors.As(err, &rpcErr) {
		return err
	}
	id, idErr := newQuestionID()
	if idErr != nil {
		return errors.Join(err, idErr)
	}
	detail, detailErr := connect.NewErrorDetail(&contractv1.Question{Id: id, Finding: question.Finding, Prompt: question.Prompt})
	if detailErr != nil {
		return errors.Join(err, detailErr)
	}
	q.mu.Lock()
	q.pending[id] = question
	q.mu.Unlock()
	rpcErr.AddDetail(detail)
	return err
}

func (q *questions) confirm(ctx context.Context, id string) error {
	q.mu.Lock()
	question, asked := q.pending[id]
	delete(q.pending, id)
	q.mu.Unlock()
	if !asked || question.Confirm == nil {
		return refusal.Refuse(refusal.CodeInvalid, "this provider is waiting on no question %q", id)
	}
	return question.Confirm(ctx)
}

func newQuestionID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(id[:]), nil
}

func (h *handlers) Confirm(ctx context.Context, req *contractv1.ConfirmRequest) (*contractv1.ConfirmResponse, error) {
	if err := h.questions.confirm(ctx, req.GetQuestionId()); err != nil {
		return nil, provider.RefusalError(err)
	}
	return &contractv1.ConfirmResponse{}, nil
}

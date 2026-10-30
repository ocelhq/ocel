package providerserver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	connect "connectrpc.com/connect"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

const maxPendingQuestions = 16

var ErrQuestionNotPending = errors.New("no such question is pending")

type PendingQuestion struct {
	ID      string
	AskedAt time.Time
}

type askedQuestion struct {
	question provider.Question
	askedAt  time.Time
}

type questions struct {
	mu      sync.Mutex
	pending map[string]askedQuestion
	order   []string
}

func newQuestions() *questions {
	return &questions{pending: map[string]askedQuestion{}}
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
	if question.Remedy == "" {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("provider: the question %q carries no remedy for a user nobody can ask: %w", question.Prompt, err))
	}
	id, idErr := newQuestionID()
	if idErr != nil {
		return errors.Join(err, idErr)
	}
	detail, detailErr := connect.NewErrorDetail(&contractv1.Question{Id: id, Finding: question.Finding, Prompt: question.Prompt, Remedy: question.Remedy})
	if detailErr != nil {
		return errors.Join(err, detailErr)
	}
	q.mu.Lock()
	q.pending[id] = askedQuestion{question: question, askedAt: time.Now()}
	q.order = append(q.order, id)
	for len(q.order) > maxPendingQuestions {
		delete(q.pending, q.order[0])
		q.order = q.order[1:]
	}
	q.mu.Unlock()
	rpcErr.AddDetail(detail)
	return err
}

func (q *questions) confirm(ctx context.Context, id string) error {
	q.mu.Lock()
	pending, asked := q.pending[id]
	delete(q.pending, id)
	q.order = slices.DeleteFunc(q.order, func(listed string) bool { return listed == id })
	q.mu.Unlock()
	if !asked || pending.question.Confirm == nil {
		return ErrQuestionNotPending
	}
	return pending.question.Confirm(ctx)
}

func (q *questions) listPending() []PendingQuestion {
	q.mu.Lock()
	defer q.mu.Unlock()
	listed := make([]PendingQuestion, 0, len(q.order))
	for _, id := range q.order {
		listed = append(listed, PendingQuestion{ID: id, AskedAt: q.pending[id].askedAt})
	}
	return listed
}

func newQuestionID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(id[:]), nil
}

func (h *handlers) Confirm(ctx context.Context, req *contractv1.ConfirmRequest) (*contractv1.ConfirmResponse, error) {
	err := h.questions.confirm(ctx, req.GetQuestionId())
	if errors.Is(err, ErrQuestionNotPending) {
		err = RefuseNotPending(req.GetQuestionId())
	}
	if err != nil {
		return nil, provider.RefusalError(err)
	}
	return &contractv1.ConfirmResponse{}, nil
}

func RefuseNotPending(questionID string) error {
	return refusal.Refuse(refusal.CodeInvalid, "this provider is waiting on no question %q", questionID)
}

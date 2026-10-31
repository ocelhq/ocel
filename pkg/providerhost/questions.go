package providerhost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/anypb"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/providerserver"
	"github.com/ocelhq/ocel/pkg/records"
)

const (
	confirmPoll = 100 * time.Millisecond
	relayEvery  = 250 * time.Millisecond
)

func questionKey(s *session, question string) records.Key {
	return providerserver.QuestionKey(s.tier, s.tenant, s.id, question)
}

type questionRecord struct {
	AskedAt time.Time `json:"askedAt"`
	Answer  []byte    `json:"answer,omitempty"`
}

type answer struct {
	Code    connect.Code   `json:"code,omitempty"`
	Message string         `json:"message,omitempty"`
	Details []answerDetail `json:"details,omitempty"`
}

type answerDetail struct {
	Type  string `json:"type"`
	Value []byte `json:"value"`
}

func answerOf(err error) answer {
	if err == nil {
		return answer{}
	}
	var wire *connect.Error
	if !errors.As(err, &wire) {
		return answer{Code: connect.CodeUnknown, Message: err.Error()}
	}
	given := answer{Code: wire.Code(), Message: wire.Message()}
	for _, detail := range wire.Details() {
		given.Details = append(given.Details, answerDetail{Type: detail.Type(), Value: detail.Bytes()})
	}
	return given
}

func (a answer) err() error {
	if a.Code == 0 {
		return nil
	}
	refused := connect.NewError(a.Code, errors.New(a.Message))
	for _, carried := range a.Details {
		if detail, err := connect.NewErrorDetail(&anypb.Any{TypeUrl: "type.googleapis.com/" + carried.Type, Value: carried.Value}); err == nil {
			refused.AddDetail(detail)
		}
	}
	return refused
}

func (h *Host) confirm(ctx context.Context, s *session, request *connect.Request[contractv1.ConfirmRequest]) (*connect.Response[contractv1.ConfirmResponse], error) {
	question := request.Msg.GetQuestionId()
	err := s.inner.Confirm(ctx, question)
	if errors.Is(err, providerserver.ErrQuestionNotPending) {
		err = h.relay(ctx, s, question, wireError(providerserver.RefuseNotPending(question)))
	}
	if err != nil {
		return nil, wireError(err)
	}
	return connect.NewResponse(&contractv1.ConfirmResponse{}), nil
}

func wireError(err error) error {
	var wire *connect.Error
	if err == nil || errors.As(err, &wire) {
		return err
	}
	return provider.RefusalError(err)
}

func (h *Host) relay(ctx context.Context, s *session, question string, refused error) error {
	if s.records == nil || s.cipher == nil {
		return refused
	}
	key := questionKey(s, question)
	value, err := json.Marshal(questionRecord{AskedAt: h.config.Now()})
	if err != nil {
		return err
	}
	if _, err := s.records.Write(ctx, records.Entry{Key: key, Value: value}); err != nil && !errors.Is(err, records.ErrStale) {
		return connect.NewError(connect.CodeUnavailable, fmt.Errorf("ask the instance holding question %s to confirm it: %w", question, err))
	}
	defer func() {
		forget, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		_ = records.Forget(forget, s.records, key)
	}()
	expired := time.NewTimer(h.config.questionWait)
	defer expired.Stop()
	poll := time.NewTicker(confirmPoll)
	defer poll.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-expired.C:
			return refused
		case <-poll.C:
		}
		entry, err := s.records.Read(ctx, key)
		if errors.Is(err, records.ErrNotFound) {
			return refused
		}
		if err != nil {
			continue
		}
		var recorded questionRecord
		if err := json.Unmarshal(entry.Value, &recorded); err != nil || recorded.Answer == nil {
			continue
		}
		plaintext, err := s.cipher.Open(ctx, s.tier, providerserver.QuestionAssociatedData(s.tenant, s.id, question), recorded.Answer)
		if err != nil {
			return connect.NewError(connect.CodeInternal, fmt.Errorf("open the answer to question %s: %w", question, err))
		}
		var given answer
		if err := json.Unmarshal(plaintext, &given); err != nil {
			return connect.NewError(connect.CodeInternal, fmt.Errorf("read the answer to question %s: %w", question, err))
		}
		return given.err()
	}
}

func (h *Host) answerRelayedQuestions(ctx context.Context) {
	for _, s := range h.listConfiguredSessions() {
		for _, pending := range s.inner.ListPendingQuestions() {
			if time.Since(pending.AskedAt) >= 2*h.config.questionWait {
				continue
			}
			entry, err := s.records.Read(ctx, questionKey(s, pending.ID))
			if err != nil {
				continue
			}
			var recorded questionRecord
			if json.Unmarshal(entry.Value, &recorded) != nil || recorded.Answer != nil {
				continue
			}
			if h.acquireForAnswer(s, pending.ID) {
				go h.answer(ctx, s, entry, pending.ID)
			}
		}
	}
}

func (h *Host) listConfiguredSessions() []*session {
	h.mu.Lock()
	defer h.mu.Unlock()
	var listed []*session
	for _, s := range h.sessions {
		if s.configured && s.records != nil && s.cipher != nil {
			listed = append(listed, s)
		}
	}
	return listed
}

func (h *Host) acquireForAnswer(s *session, question string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if current, ok := h.sessions[s.key]; !ok || current != s || s.answering[question] {
		return false
	}
	s.calls++
	s.answering[question] = true
	return true
}

func (h *Host) answer(ctx context.Context, s *session, entry records.Entry, question string) {
	defer func() {
		h.mu.Lock()
		delete(s.answering, question)
		h.mu.Unlock()
		h.leave(s)
	}()
	err := s.inner.Confirm(ctx, question)
	if errors.Is(err, providerserver.ErrQuestionNotPending) {
		return
	}
	plaintext, err := json.Marshal(answerOf(wireError(err)))
	if err != nil {
		return
	}
	sealed, err := s.cipher.Seal(ctx, s.tier, providerserver.QuestionAssociatedData(s.tenant, s.id, question), plaintext)
	if err != nil {
		return
	}
	var recorded questionRecord
	if json.Unmarshal(entry.Value, &recorded) != nil {
		return
	}
	recorded.Answer = sealed
	if entry.Value, err = json.Marshal(recorded); err != nil {
		return
	}
	_, _ = s.records.Write(ctx, entry)
}

func (h *Host) abandonedEntry(entry records.Entry) (bool, error) {
	var recorded questionRecord
	if err := json.Unmarshal(entry.Value, &recorded); err != nil {
		return true, nil
	}
	return h.config.Now().Sub(recorded.AskedAt) >= 2*h.config.questionWait, nil
}

func (h *Host) relayAndSweep(ctx context.Context) {
	defer close(h.stopped)
	tick := time.NewTicker(relayEvery)
	defer tick.Stop()
	var swept time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		h.answerRelayedQuestions(ctx)
		if time.Since(swept) >= sweepEvery {
			h.sweepRecords(ctx)
			swept = time.Now()
		}
	}
}

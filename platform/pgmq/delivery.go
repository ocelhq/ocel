package pgmq

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/encoding/protojson"

	topicv1 "github.com/ocelhq/ocel/pkg/proto/app/topic/v1"
)

const (
	envelopeVersion  = 1
	maxAnswerBytes   = 1 << 20
	maxErrorExcerpt  = 512
	newRevisionSQL   = "md5(random()::text || clock_timestamp()::text)"
	stagedPayloadSQL = "(SELECT value FROM ocel.records WHERE purpose = 'staged-payload' AND topic = ocel.runs.topic AND key = ocel.runs.message_id)"
)

var (
	errTimedOut    = errors.New("the attempt ran past its maxDuration")
	errRunCanceled = errors.New("the run was canceled")

	schemaDigest = regexp.MustCompile(`^sha256-[0-9a-f]{64}$`)
)

type claim struct {
	msg              message
	execution        string
	messageID        string
	publishedAt      time.Time
	attempt          int
	maxAttempts      int
	firstAttemptedAt time.Time
	payload          json.RawMessage
}

type outcome int

const (
	succeeded outcome = iota
	aborted
	timedOut
	failed
	canceled
	interrupted
)

type result struct {
	outcome outcome
	output  json.RawMessage
	reason  string
}

func (e *Engine) deliver(ctx context.Context, q *queueState, ref consumerRef, worker Worker, messages []message) {
	var claims []claim
	for _, m := range messages {
		c, ok, err := e.claim(ctx, q, m)
		if err != nil {
			if ctx.Err() == nil {
				slog.Warn("claim a run", "queue", q.name, "execution", m.body.Execution, "error", err)
			}
			q.drop(m.id)
			continue
		}
		if !ok {
			q.drop(m.id)
			continue
		}
		claims = append(claims, c)
	}
	if len(claims) == 0 {
		return
	}
	attemptCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	if maxDuration := ref.consumer.GetMaxDuration().AsDuration(); maxDuration > 0 {
		var stop context.CancelFunc
		attemptCtx, stop = context.WithTimeoutCause(attemptCtx, maxDuration, errTimedOut)
		defer stop()
	}
	if len(claims) == 1 {
		e.track(claims[0].execution, cancel)
		defer e.untrack(claims[0].execution)
	}
	res := e.post(ctx, attemptCtx, worker.URL, envelopeOf(ref, claims, ref.consumer.GetBatch().GetSize() > 0))
	for _, c := range claims {
		if err := e.finish(ctx, q, ref, c, res); err != nil && ctx.Err() == nil {
			slog.Warn("record an attempt's outcome", "queue", q.name, "execution", c.execution, "error", err)
		}
		q.drop(c.msg.id)
	}
}

func (e *Engine) track(execution string, cancel context.CancelCauseFunc) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.inFlight[execution] = func() { cancel(errRunCanceled) }
}

func (e *Engine) untrack(execution string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.inFlight, execution)
}

func (e *Engine) stopInFlight(execution string) {
	e.mu.Lock()
	stop := e.inFlight[execution]
	e.mu.Unlock()
	if stop != nil {
		stop()
	}
}

func (e *Engine) claim(ctx context.Context, q *queueState, m message) (claim, bool, error) {
	c := claim{msg: m, execution: m.body.Execution}
	var first *time.Time
	err := e.pool.QueryRow(ctx, `
		UPDATE ocel.runs SET status = 'executing', attempts = attempts + 1, error = '',
			started_at = COALESCE(started_at, clock_timestamp()), revision = `+newRevisionSQL+`
		WHERE execution = $1 AND status IN ('queued', 'delayed', 'executing')
			AND (expires_at IS NULL OR expires_at > clock_timestamp() OR attempts > 0)
		RETURNING message_id, published_at, attempts, max_attempts, started_at, COALESCE(payload, `+stagedPayloadSQL+`)`,
		c.execution,
	).Scan(&c.messageID, &c.publishedAt, &c.attempt, &c.maxAttempts, &first, &c.payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return claim{}, false, e.release(ctx, q.name, m)
	}
	if err != nil {
		return claim{}, false, err
	}
	c.firstAttemptedAt = timeOf(first)
	return c, true, nil
}

func (e *Engine) release(ctx context.Context, queue string, m message) error {
	return e.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			UPDATE ocel.runs SET status = 'expired', finished_at = clock_timestamp(), revision = `+newRevisionSQL+`
			WHERE execution = $1 AND status IN ('queued', 'delayed') AND attempts = 0 AND expires_at <= clock_timestamp()`,
			m.body.Execution); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, "SELECT pgmq.delete($1, $2::bigint)", queue, m.id)
		return err
	})
}

func envelopeOf(ref consumerRef, claims []claim, batch bool) *topicv1.Envelope {
	envelope := &topicv1.Envelope{
		V:        envelopeVersion,
		Topic:    ref.topicName,
		Consumer: ref.consumer.GetName(),
		Schema:   schemaOf(ref.topic.GetSchema()),
	}
	if !batch {
		c := claims[0]
		envelope.Execution = c.execution
		envelope.Message = messageOf(c)
		envelope.Attempt = attemptOf(c)
		envelope.Payload = valueOf(c.payload)
		return envelope
	}
	for _, c := range claims {
		envelope.Messages = append(envelope.Messages, &topicv1.Delivery{
			Execution: c.execution,
			Message:   messageOf(c),
			Attempt:   attemptOf(c),
			Payload:   valueOf(c.payload),
		})
	}
	return envelope
}

func messageOf(c claim) *topicv1.Message {
	return &topicv1.Message{Id: c.messageID, PublishedAt: timestampOf(c.publishedAt)}
}

func attemptOf(c claim) *topicv1.Attempt {
	return &topicv1.Attempt{Number: int32(c.attempt), Of: int32(c.maxAttempts), FirstAttemptedAt: timestampOf(c.firstAttemptedAt)}
}

func schemaOf(schema string) string {
	if schema == "" || schemaDigest.MatchString(schema) {
		return schema
	}
	sum := sha256.Sum256([]byte(schema))
	return "sha256-" + hex.EncodeToString(sum[:])
}

type abortAnswer struct {
	Abort *struct {
		Reason string `json:"reason"`
	} `json:"abort"`
}

func (e *Engine) post(ctx, attemptCtx context.Context, url string, envelope *topicv1.Envelope) result {
	body, err := protojson.Marshal(envelope)
	if err != nil {
		return result{outcome: failed, reason: fmt.Sprintf("encode the envelope: %v", err)}
	}
	req, err := http.NewRequestWithContext(attemptCtx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return result{outcome: failed, reason: fmt.Sprintf("address the worker: %v", err)}
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return interruption(ctx, attemptCtx, err)
	}
	defer resp.Body.Close()
	answer, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswerBytes))
	if err != nil {
		return interruption(ctx, attemptCtx, err)
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if !json.Valid(answer) {
			answer = nil
		}
		return result{outcome: succeeded, output: answer}
	}
	var abort abortAnswer
	if json.Unmarshal(answer, &abort) == nil && abort.Abort != nil {
		return result{outcome: aborted, reason: abort.Abort.Reason}
	}
	excerpt := answer
	if len(excerpt) > maxErrorExcerpt {
		excerpt = excerpt[:maxErrorExcerpt]
	}
	return result{outcome: failed, reason: fmt.Sprintf("the worker answered %s: %s", resp.Status, bytes.TrimSpace(excerpt))}
}

func interruption(ctx, attemptCtx context.Context, err error) result {
	switch cause := context.Cause(attemptCtx); {
	case errors.Is(cause, errTimedOut):
		return result{outcome: timedOut, reason: cause.Error()}
	case errors.Is(cause, errRunCanceled):
		return result{outcome: canceled}
	case ctx.Err() != nil:
		return result{outcome: interrupted}
	}
	return result{outcome: failed, reason: fmt.Sprintf("reach the worker: %v", err)}
}

func (e *Engine) finish(ctx context.Context, q *queueState, ref consumerRef, c claim, res result) error {
	switch res.outcome {
	case canceled, interrupted:
		return nil
	case succeeded:
		return e.settle(ctx, q.name, c, "completed", res.output, "")
	case aborted:
		return e.settle(ctx, q.name, c, "failed", nil, res.reason)
	case timedOut:
		return e.settle(ctx, q.name, c, "timed-out", nil, res.reason)
	}
	if c.attempt >= c.maxAttempts {
		return e.settle(ctx, q.name, c, "failed", nil, res.reason)
	}
	retryAt := time.Now().Add(retryPolicyOf(ref).backoff(c.attempt, jitter()))
	return e.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE ocel.runs SET status = 'queued', error = $2, revision = `+newRevisionSQL+`
			WHERE execution = $1 AND status = 'executing'`, c.execution, res.reason)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		_, err = tx.Exec(ctx, "SELECT pgmq.set_vt($1, $2::bigint, $3::timestamptz)", q.name, c.msg.id, retryAt)
		return err
	})
}

func (e *Engine) settle(ctx context.Context, queue string, c claim, status string, output json.RawMessage, reason string) error {
	return e.inTx(ctx, func(tx pgx.Tx) error {
		payload := "payload"
		if status != "completed" {
			payload = "COALESCE(payload, " + stagedPayloadSQL + ")"
		}
		if _, err := tx.Exec(ctx, `UPDATE ocel.runs SET status = $2, output = $3, error = $4, payload = `+payload+`,
			finished_at = clock_timestamp(), revision = `+newRevisionSQL+`
			WHERE execution = $1 AND status = 'executing'`, c.execution, status, jsonOrNull(output), reason); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, "SELECT pgmq.delete($1, $2::bigint)", queue, c.msg.id)
		return err
	})
}

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
	"google.golang.org/protobuf/proto"

	topicv1 "github.com/ocelhq/ocel/pkg/proto/app/topic/v1"
)

const (
	envelopeVersion      = 1
	maxAnswerBytes       = 1 << 20
	maxOutputBytes       = 256 << 10
	maxErrorExcerptBytes = 512
	newRevisionSQL       = "md5(random()::text || clock_timestamp()::text)"
	stagedPayloadSQL     = "(SELECT value FROM ocel.records WHERE purpose = 'staged-payload' AND topic = ocel.runs.topic AND key = ocel.runs.message_id)"
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
	refused
)

type result struct {
	outcome outcome
	output  json.RawMessage
	reason  string
}

func (e *Engine) deliver(ctx context.Context, loop *queueLoop, deployed deployedConsumer, worker Worker, messages []message) {
	var claims []claim
	for _, msg := range messages {
		claimed, ok, err := e.claim(ctx, loop, msg)
		if err != nil {
			if ctx.Err() == nil {
				slog.Warn("claim a run", "queue", loop.name, "execution", msg.body.Execution, "error", err)
			}
			loop.drop(msg.id)
			continue
		}
		if !ok {
			loop.drop(msg.id)
			continue
		}
		claims = append(claims, claimed)
	}
	if len(claims) == 0 {
		return
	}
	attemptCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	if maxDuration := deployed.consumer.GetMaxDuration().AsDuration(); maxDuration > 0 {
		var stop context.CancelFunc
		attemptCtx, stop = context.WithTimeoutCause(attemptCtx, maxDuration, errTimedOut)
		defer stop()
	}
	if len(claims) == 1 {
		e.track(claims[0].execution, cancel)
		defer e.untrack(claims[0].execution)
	}
	res := e.post(ctx, attemptCtx, worker.URL, envelopeOf(deployed, claims, deployed.consumer.GetBatch().GetSize() > 0))
	for _, claimed := range claims {
		loop.drop(claimed.msg.id)
		if err := e.finish(ctx, loop, deployed, claimed, res); err != nil && ctx.Err() == nil {
			slog.Warn("record an attempt's outcome", "queue", loop.name, "execution", claimed.execution, "error", err)
		}
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

func (e *Engine) claim(ctx context.Context, loop *queueLoop, msg message) (claim, bool, error) {
	claimed := claim{msg: msg, execution: msg.body.Execution}
	var first *time.Time
	err := e.pool.QueryRow(ctx, `
		UPDATE ocel.runs SET status = 'executing', attempts = attempts + 1, error = '',
			started_at = COALESCE(started_at, clock_timestamp()), revision = `+newRevisionSQL+`
		WHERE execution = $1 AND status IN ('queued', 'delayed', 'executing')
			AND (expires_at IS NULL OR expires_at > clock_timestamp() OR attempts > 0)
		RETURNING message_id, published_at, attempts, max_attempts, started_at, COALESCE(payload, `+stagedPayloadSQL+`)`,
		claimed.execution,
	).Scan(&claimed.messageID, &claimed.publishedAt, &claimed.attempt, &claimed.maxAttempts, &first, &claimed.payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return claim{}, false, e.discard(ctx, loop.name, msg)
	}
	if err != nil {
		return claim{}, false, err
	}
	claimed.firstAttemptedAt = timeOf(first)
	return claimed, true, nil
}

func (e *Engine) discard(ctx context.Context, queue string, msg message) error {
	return e.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			UPDATE ocel.runs SET status = 'expired', finished_at = clock_timestamp(), revision = `+newRevisionSQL+`
			WHERE execution = $1 AND status IN ('queued', 'delayed') AND attempts = 0 AND expires_at <= clock_timestamp()`,
			msg.body.Execution); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, "SELECT pgmq.delete($1, $2::bigint)", queue, msg.id)
		return err
	})
}

func envelopeOf(deployed deployedConsumer, claims []claim, batch bool) *topicv1.Envelope {
	envelope := &topicv1.Envelope{
		V:        envelopeVersion,
		Topic:    deployed.topicName,
		Consumer: deployed.consumer.GetName(),
		Schema:   schemaOf(deployed.topic.GetSchema()),
	}
	if !batch {
		claimed := claims[0]
		envelope.Execution = claimed.execution
		envelope.Message = messageOf(claimed)
		envelope.Attempt = attemptOf(claimed)
		envelope.Payload = claimed.payload
		return envelope
	}
	for _, claimed := range claims {
		envelope.Messages = append(envelope.Messages, &topicv1.Delivery{
			Execution: claimed.execution,
			Message:   messageOf(claimed),
			Attempt:   attemptOf(claimed),
			Payload:   claimed.payload,
		})
	}
	return envelope
}

func messageOf(claimed claim) *topicv1.Message {
	return &topicv1.Message{Id: claimed.messageID, PublishedAt: timestampOf(claimed.publishedAt)}
}

func attemptOf(claimed claim) *topicv1.Attempt {
	return &topicv1.Attempt{Number: int32(claimed.attempt), Of: int32(claimed.maxAttempts), FirstAttemptedAt: timestampOf(claimed.firstAttemptedAt)}
}

func schemaOf(schema string) string {
	if schema == "" || schemaDigest.MatchString(schema) {
		return schema
	}
	sum := sha256.Sum256([]byte(schema))
	return "sha256-" + hex.EncodeToString(sum[:])
}

func encodeEnvelope(envelope *topicv1.Envelope) ([]byte, error) {
	bare := proto.CloneOf(envelope)
	bare.Payload = nil
	for _, delivery := range bare.GetMessages() {
		delivery.Payload = nil
	}
	encoded, err := protojson.Marshal(bare)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		return nil, err
	}
	if len(envelope.GetPayload()) > 0 {
		fields["payload"] = envelope.GetPayload()
	}
	if len(envelope.GetMessages()) > 0 {
		var deliveries []map[string]json.RawMessage
		if err := json.Unmarshal(fields["messages"], &deliveries); err != nil {
			return nil, err
		}
		for i, delivery := range envelope.GetMessages() {
			if len(delivery.GetPayload()) > 0 {
				deliveries[i]["payload"] = delivery.GetPayload()
			}
		}
		if fields["messages"], err = encodeJSONVerbatim(deliveries); err != nil {
			return nil, err
		}
	}
	return encodeJSONVerbatim(fields)
}

func encodeJSONVerbatim(value any) ([]byte, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

func (e *Engine) post(ctx, attemptCtx context.Context, url string, envelope *topicv1.Envelope) result {
	body, err := encodeEnvelope(envelope)
	if err != nil {
		return result{outcome: refused, reason: fmt.Sprintf("encode the envelope: %v", err)}
	}
	req, err := http.NewRequestWithContext(attemptCtx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return result{outcome: failed, reason: fmt.Sprintf("address the worker: %v", err)}
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return classifyInterruption(ctx, attemptCtx, err)
	}
	defer resp.Body.Close()
	answer, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswerBytes))
	if err != nil {
		return classifyInterruption(ctx, attemptCtx, err)
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if len(answer) > maxOutputBytes {
			return result{outcome: refused, reason: "the worker answered an output over the 256 KiB a run may store"}
		}
		if !json.Valid(answer) {
			answer = nil
		}
		return result{outcome: succeeded, output: answer}
	}
	var decoded topicv1.Answer
	if protojson.Unmarshal(answer, &decoded) == nil && decoded.GetAbort() != nil {
		return result{outcome: aborted, reason: decoded.GetAbort().GetReason()}
	}
	excerpt := answer
	if len(excerpt) > maxErrorExcerptBytes {
		excerpt = excerpt[:maxErrorExcerptBytes]
	}
	return result{outcome: failed, reason: fmt.Sprintf("the worker answered %s: %s", resp.Status, bytes.TrimSpace(excerpt))}
}

func classifyInterruption(ctx, attemptCtx context.Context, err error) result {
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

func (e *Engine) finish(ctx context.Context, loop *queueLoop, deployed deployedConsumer, claimed claim, res result) error {
	switch res.outcome {
	case canceled, interrupted:
		return nil
	case succeeded:
		return e.settle(ctx, loop.name, claimed, "completed", res.output, "")
	case aborted, refused:
		return e.settle(ctx, loop.name, claimed, "failed", nil, res.reason)
	case timedOut:
		if isTask(deployed.topic) {
			return e.settle(ctx, loop.name, claimed, "timed-out", nil, res.reason)
		}
	}
	if claimed.attempt >= claimed.maxAttempts {
		return e.settle(ctx, loop.name, claimed, "failed", nil, res.reason)
	}
	retryAt := time.Now().Add(retryPolicyOf(deployed).backoff(claimed.attempt, jitter()))
	return e.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE ocel.runs SET status = 'queued', error = $2, revision = `+newRevisionSQL+`
			WHERE execution = $1 AND status = 'executing'`, claimed.execution, res.reason)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		_, err = tx.Exec(ctx, "SELECT pgmq.set_vt($1, $2::bigint, $3::timestamptz)", loop.name, claimed.msg.id, retryAt)
		return err
	})
}

func (e *Engine) settle(ctx context.Context, queue string, claimed claim, status string, output json.RawMessage, reason string) error {
	return e.inTx(ctx, func(tx pgx.Tx) error {
		payload := "payload"
		if status != "completed" {
			payload = "COALESCE(payload, " + stagedPayloadSQL + ")"
		}
		if _, err := tx.Exec(ctx, `UPDATE ocel.runs SET status = $2, output = $3, error = $4, payload = `+payload+`,
			finished_at = clock_timestamp(), revision = `+newRevisionSQL+`
			WHERE execution = $1 AND status = 'executing'`, claimed.execution, status, jsonOrNull(output), reason); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, "SELECT pgmq.delete($1, $2::bigint)", queue, claimed.msg.id)
		return err
	})
}

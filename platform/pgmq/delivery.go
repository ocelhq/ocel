package pgmq

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ocelhq/ocel/pkg/envelope"
	topicv1 "github.com/ocelhq/ocel/pkg/proto/app/topic/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/taskruns"
)

const (
	newRevisionSQL   = "md5(random()::text || clock_timestamp()::text)"
	stagedPayloadSQL = "(SELECT value FROM ocel.records WHERE purpose = 'staged-payload' AND topic = ocel.runs.topic AND key = ocel.runs.message_id)"
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
		attemptCtx, stop = context.WithTimeoutCause(attemptCtx, maxDuration, envelope.ErrTimedOut)
		defer stop()
	}
	if len(claims) == 1 {
		e.track(claims[0].execution, cancel)
		defer e.untrack(claims[0].execution)
	}
	posted := time.Now()
	res := envelope.Post(ctx, attemptCtx, http.DefaultClient, worker.URL, envelopeOf(deployed, claims, deployed.consumer.GetBatch().GetSize() > 0))
	took := time.Since(posted)
	for _, claimed := range claims {
		loop.drop(claimed.msg.id)
		status, err := e.finish(ctx, loop, deployed, claimed, res)
		if err != nil {
			if ctx.Err() == nil {
				slog.Warn("record an attempt's outcome", "queue", loop.name, "execution", claimed.execution, "error", err)
			}
			continue
		}
		if status != "" {
			e.reportAttempt(Attempt{
				Topic:       deployed.topicName,
				Consumer:    deployed.consumer.GetName(),
				IsTask:      taskruns.IsTask(deployed.topic),
				Execution:   claimed.execution,
				Number:      claimed.attempt,
				MaxAttempts: claimed.maxAttempts,
				Status:      status,
				Took:        took,
				Reason:      res.Reason,
			})
		}
	}
}

func (e *Engine) track(execution string, cancel context.CancelCauseFunc) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.inFlight[execution] = func() { cancel(envelope.ErrCanceled) }
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
	delivered := &topicv1.Envelope{
		V:        envelope.Version,
		Topic:    deployed.topicName,
		Consumer: deployed.consumer.GetName(),
		Schema:   envelope.SchemaOf(deployed.topic.GetSchema()),
	}
	if !batch {
		claimed := claims[0]
		delivered.Execution = claimed.execution
		delivered.Message = messageOf(claimed)
		delivered.Attempt = attemptOf(claimed)
		delivered.Payload = claimed.payload
		return delivered
	}
	for _, claimed := range claims {
		delivered.Messages = append(delivered.Messages, &topicv1.Delivery{
			Execution: claimed.execution,
			Message:   messageOf(claimed),
			Attempt:   attemptOf(claimed),
			Payload:   claimed.payload,
		})
	}
	return delivered
}

func messageOf(claimed claim) *topicv1.Message {
	return &topicv1.Message{Id: claimed.messageID, PublishedAt: taskruns.TimestampOf(claimed.publishedAt)}
}

func attemptOf(claimed claim) *topicv1.Attempt {
	return &topicv1.Attempt{Number: int32(claimed.attempt), Of: int32(claimed.maxAttempts), FirstAttemptedAt: taskruns.TimestampOf(claimed.firstAttemptedAt)}
}

func (e *Engine) finish(ctx context.Context, loop *queueLoop, deployed deployedConsumer, claimed claim, res envelope.Result) (provider.RunStatus, error) {
	switch res.Outcome {
	case envelope.Canceled, envelope.Interrupted:
		return "", nil
	case envelope.Succeeded:
		return provider.RunCompleted, e.settle(ctx, loop.name, claimed, provider.RunCompleted, res.Output, "")
	case envelope.Aborted, envelope.Refused:
		return provider.RunFailed, e.settle(ctx, loop.name, claimed, provider.RunFailed, nil, res.Reason)
	case envelope.TimedOut:
		if taskruns.IsTask(deployed.topic) {
			return provider.RunTimedOut, e.settle(ctx, loop.name, claimed, provider.RunTimedOut, nil, res.Reason)
		}
	}
	if claimed.attempt >= claimed.maxAttempts {
		return provider.RunFailed, e.settle(ctx, loop.name, claimed, provider.RunFailed, nil, res.Reason)
	}
	retryAt := time.Now().Add(taskruns.Backoff(retryPolicyOf(deployed), claimed.attempt, taskruns.Jitter()))
	return provider.RunQueued, e.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE ocel.runs SET status = 'queued', error = $2, revision = `+newRevisionSQL+`
			WHERE execution = $1 AND status = 'executing'`, claimed.execution, res.Reason)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		_, err = tx.Exec(ctx, "SELECT pgmq.set_vt($1, $2::bigint, $3::timestamptz)", loop.name, claimed.msg.id, retryAt)
		return err
	})
}

func (e *Engine) settle(ctx context.Context, queue string, claimed claim, status provider.RunStatus, output json.RawMessage, reason string) error {
	return e.inTx(ctx, func(tx pgx.Tx) error {
		payload := "payload"
		if status != provider.RunCompleted {
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

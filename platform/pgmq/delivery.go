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
	"github.com/ocelhq/ocel/pkg/runs"
)

var (
	claimMove = runMove{
		from:  unfinishedStatuses,
		to:    provider.RunExecuting,
		where: "(expires_at IS NULL OR expires_at > clock_timestamp() OR attempts > 0) AND attempts < max_attempts AND (due_at IS NULL OR due_at <= clock_timestamp())",
		set:   "attempts = attempts + 1, error = '', started_at = COALESCE(started_at, clock_timestamp())",
	}
	exhaustMove = guarded(settleMove(provider.RunFailed, nil, "its last attempt was cut off before the worker answered, and it has no attempts left"), "attempts >= max_attempts")
	expireMove  = runMove{
		from:    waitingStatuses,
		to:      provider.RunExpired,
		where:   "attempts = 0 AND expires_at <= clock_timestamp()",
		set:     "finished_at = clock_timestamp()",
		message: deleteMessage,
	}
)

func guarded(m runMove, where string) runMove {
	m.where = where
	return m
}

func stagedPayloadOf(run string) string {
	return "(SELECT value FROM ocel.records WHERE purpose = 'staged-payload' AND topic = " + run + ".topic AND key = " + run + ".message_id)"
}

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
		claimed, ok, err := e.claim(ctx, msg)
		if err == nil && !ok {
			loop.drop(msg.id)
			err = e.settleUnclaimable(ctx, loop.name, msg)
		}
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
	res := envelope.Post(ctx, attemptCtx, clientFor(worker), worker.URL, envelopeOf(deployed, claims, deployed.consumer.GetBatch().GetSize() > 0))
	took := time.Since(posted)
	for _, claimed := range claims {
		loop.drop(claimed.msg.id)
		status, err := e.finish(ctx, deployed, claimed, res)
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
				IsTask:      isTask(deployed.topic),
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

func (e *Engine) claim(ctx context.Context, msg message) (claim, bool, error) {
	claimed := claim{msg: msg, execution: msg.body.Execution}
	var first *time.Time
	var kept any
	query, args := claimMove.of(claimed.execution).statement("message_id, published_at, attempts, max_attempts, started_at, COALESCE(payload, " + stagedPayloadOf("moved") + ")")
	err := e.pool.QueryRow(ctx, query, args).
		Scan(&kept, &claimed.messageID, &claimed.publishedAt, &claimed.attempt, &claimed.maxAttempts, &first, &claimed.payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return claim{}, false, nil
	}
	if err != nil {
		return claim{}, false, err
	}
	claimed.firstAttemptedAt = timeOf(first)
	return claimed, true, nil
}

func (e *Engine) settleUnclaimable(ctx context.Context, queue string, msg message) error {
	for _, settled := range []runMove{expireMove, exhaustMove} {
		moved, err := moveRun(ctx, e.pool, msg.body.Execution, settled)
		if err != nil || moved {
			return err
		}
	}
	var hidden bool
	err := e.pool.QueryRow(ctx, `SELECT count(*) > 0 FROM ocel.runs, pgmq.set_vt($1, $2::bigint, due_at)
		WHERE execution = $3 AND status = ANY($4) AND due_at > clock_timestamp()`,
		queue, msg.id, msg.body.Execution, statusNames(waitingStatuses)).Scan(&hidden)
	if err != nil || hidden {
		return err
	}
	_, err = e.pool.Exec(ctx, "SELECT pgmq.delete($1, $2::bigint)", queue, msg.id)
	return err
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
	return &topicv1.Message{Id: claimed.messageID, PublishedAt: runs.TimestampOf(claimed.publishedAt)}
}

func attemptOf(claimed claim) *topicv1.Attempt {
	return &topicv1.Attempt{Number: int32(claimed.attempt), Of: int32(claimed.maxAttempts), FirstAttemptedAt: runs.TimestampOf(claimed.firstAttemptedAt)}
}

func (e *Engine) finish(ctx context.Context, deployed deployedConsumer, claimed claim, res envelope.Result) (provider.RunStatus, error) {
	move := moveAfter(deployed, claimed, res)
	if move.to == "" {
		return "", nil
	}
	moved, err := moveRun(ctx, e.pool, claimed.execution, move)
	if err != nil || !moved {
		return "", err
	}
	return move.to, nil
}

func moveAfter(deployed deployedConsumer, claimed claim, res envelope.Result) runMove {
	switch res.Outcome {
	case envelope.Canceled, envelope.Interrupted:
		return runMove{}
	case envelope.Succeeded:
		return settleMove(provider.RunCompleted, res.Output, "")
	case envelope.Aborted, envelope.Refused:
		return settleMove(provider.RunFailed, nil, res.Reason)
	case envelope.TimedOut:
		if isTask(deployed.topic) {
			return settleMove(provider.RunTimedOut, nil, res.Reason)
		}
	}
	if claimed.attempt >= claimed.maxAttempts {
		return settleMove(provider.RunFailed, nil, res.Reason)
	}
	return runMove{
		from:    []provider.RunStatus{provider.RunExecuting},
		to:      provider.RunQueued,
		set:     "error = @reason",
		args:    pgx.NamedArgs{"reason": res.Reason, "visible_at": time.Now().Add(runs.Backoff(retryPolicyOf(deployed), claimed.attempt, runs.Jitter()))},
		message: delayMessage,
	}
}

func settleMove(status provider.RunStatus, output json.RawMessage, reason string) runMove {
	payload := "payload"
	if status != provider.RunCompleted {
		payload = "COALESCE(payload, " + stagedPayloadOf("ocel.runs") + ")"
	}
	return runMove{
		from:    []provider.RunStatus{provider.RunExecuting},
		to:      status,
		set:     "output = @output, error = @reason, payload = " + payload + ", finished_at = clock_timestamp()",
		args:    pgx.NamedArgs{"output": jsonOrNull(output), "reason": reason},
		message: deleteMessage,
	}
}

func clientFor(worker Worker) *http.Client {
	if len(worker.Header) == 0 {
		return http.DefaultClient
	}
	return &http.Client{Transport: headerTransport{header: worker.Header, next: http.DefaultTransport}}
}

type headerTransport struct {
	header http.Header
	next   http.RoundTripper
}

func (t headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	for name, values := range t.header {
		for _, value := range values {
			req.Header.Add(name, value)
		}
	}
	return t.next.RoundTrip(req)
}

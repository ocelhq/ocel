package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/ocelhq/ocel/pkg/envelope"
	taskv1 "github.com/ocelhq/ocel/pkg/proto/app/task/v1"
	topicv1 "github.com/ocelhq/ocel/pkg/proto/app/topic/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/runs"
	"github.com/ocelhq/ocel/platform/aws/provider/queues"
)

const (
	dueSlack        = 250 * time.Millisecond
	slotWait        = time.Second
	maxSlotHold     = 5 * time.Minute
	cancelPoll      = 500 * time.Millisecond
	leaseMargin     = 30 * time.Second
	settleMargin    = 3 * time.Second
	inPlaceRetryCap = 30 * time.Second
)

type received struct {
	record    events.SQSMessage
	msg       queueMessage
	execution string
	run       runItem
}

type takenSlot struct {
	set   slotSet
	index string
}

type disposition int

const (
	acknowledged disposition = iota
	retained
)

func queueOf(arn string) string {
	return arn[strings.LastIndex(arn, ":")+1:]
}

func (e *Engine) Deliver(ctx context.Context, event events.SQSEvent) events.SQSEventResponse {
	var resp events.SQSEventResponse
	batches := map[string][]events.SQSMessage{}
	var order []string
	for _, record := range event.Records {
		queue := queueOf(record.EventSourceARN)
		if _, seen := batches[queue]; !seen {
			order = append(order, queue)
		}
		batches[queue] = append(batches[queue], record)
	}
	for _, queue := range order {
		records := batches[queue]
		deployed, found := e.consumerOnQueue(queue)
		if !found {
			slog.Warn("a message arrived from a queue this deployment does not consume", "queue", queue)
			for _, record := range records {
				resp.BatchItemFailures = append(resp.BatchItemFailures, events.SQSBatchItemFailure{ItemIdentifier: record.MessageId})
			}
			continue
		}
		var retainedIDs []string
		if deployed.consumer.Batch != nil && deployed.consumer.Batch.Size > 0 {
			retainedIDs = e.deliverBatch(ctx, deployed, records)
		} else {
			for i, record := range records {
				if e.deliverOne(ctx, deployed, record) == retained {
					retainedIDs = append(retainedIDs, record.MessageId)
					if deployed.fifo() {
						for _, rest := range records[i+1:] {
							retainedIDs = append(retainedIDs, rest.MessageId)
						}
						break
					}
				}
			}
		}
		for _, id := range retainedIDs {
			resp.BatchItemFailures = append(resp.BatchItemFailures, events.SQSBatchItemFailure{ItemIdentifier: id})
		}
	}
	return resp
}

func (e *Engine) recordArrival(ctx context.Context, deployed deployedConsumer, record events.SQSMessage) (received, error) {
	var msg queueMessage
	if err := json.Unmarshal([]byte(record.Body), &msg); err != nil {
		return received{}, fmt.Errorf("message %s is not one this engine sent: %w", record.MessageId, err)
	}
	got := received{record: record, msg: msg, execution: msg.executionFor(deployed.consumer.Name)}
	if msg.Execution == "" && msg.Message != "" {
		if err := e.recordConsumerRun(ctx, deployed, msg, got.execution); err != nil && !errors.Is(err, errConditionFailed) {
			return received{}, err
		}
	}
	item, found, err := e.store.readRun(ctx, deployed.topicName, got.execution, true)
	if err != nil {
		return received{}, err
	}
	if !found {
		return received{}, errRunGone
	}
	got.run = item
	return got, nil
}

var errRunGone = errors.New("the run this message carries no longer exists")

func (e *Engine) recordConsumerRun(ctx context.Context, deployed deployedConsumer, msg queueMessage, execution string) error {
	now := time.Now()
	status := provider.RunQueued
	if msg.DueAtMicros > now.UnixMicro() {
		status = provider.RunDelayed
	}
	return e.store.putRun(ctx, runItem{
		SK:                execution,
		Topic:             deployed.topicName,
		Consumer:          deployed.consumer.Name,
		Status:            string(status),
		MaxAttempts:       deployed.retry().MaxAttempts,
		CreatedAtMicros:   msg.PublishedAtMicros,
		DueAtMicros:       msg.DueAtMicros,
		Message:           msg.Message,
		PublishedAtMicros: msg.PublishedAtMicros,
		Key:               msg.Key,
		Lane:              string(runs.LaneFor(deployed.consumer.Lanes, laneOf(msg.Lane))),
	})
}

type readiness int

const (
	runnable readiness = iota
	stale
	early
	held
	expired
)

func readinessOf(got received, now time.Time) (readiness, time.Duration) {
	run := got.run
	status := provider.RunStatus(run.Status)
	switch {
	case !isUnfinished(status) || run.Delivery != got.msg.Delivery:
		return stale, 0
	case status == provider.RunExecuting && run.LeaseUntilMicros >= now.UnixMicro():
		return held, time.UnixMicro(run.LeaseUntilMicros).Sub(now)
	case status != provider.RunExecuting && run.Attempts == 0 && run.RunExpiresAtMicros > 0 && run.RunExpiresAtMicros <= now.UnixMicro():
		return expired, 0
	case status != provider.RunExecuting && run.DueAtMicros > now.Add(dueSlack).UnixMicro():
		return early, time.UnixMicro(run.DueAtMicros).Sub(now)
	}
	return runnable, 0
}

func (e *Engine) setAsideUnlessRunnable(ctx context.Context, deployed deployedConsumer, got received) (bool, disposition) {
	readiness, wait := readinessOf(got, time.Now())
	switch readiness {
	case stale:
		return false, acknowledged
	case expired:
		now := time.Now()
		_, err := e.store.updateRun(ctx, deployed.topicName, got.execution, change{
			set:        map[string]any{"status": string(provider.RunExpired), "finished_at": now.UnixMicro(), "expires_at": now.Add(runRetention).Unix()},
			condition:  "#status IN (:queued, :delayed) AND attempts = :none",
			conditions: map[string]any{":queued": string(provider.RunQueued), ":delayed": string(provider.RunDelayed), ":none": 0},
		})
		if err != nil && !errors.Is(err, errConditionFailed) {
			slog.Warn("expire a run", "execution", got.execution, "error", err)
			return false, retained
		}
		return false, acknowledged
	case held:
		return false, e.holdInQueue(ctx, deployed, got, wait)
	case early:
		if deployed.fifo() {
			e.recordReceipt(ctx, deployed, got)
		}
		return false, e.deferMessage(ctx, deployed, got, wait)
	}
	return true, acknowledged
}

func (e *Engine) recordReceipt(ctx context.Context, deployed deployedConsumer, got received) {
	_, err := e.store.updateRun(ctx, deployed.topicName, got.execution, change{
		set:        map[string]any{"receipt": got.record.ReceiptHandle},
		condition:  "#delivery = :token",
		conditions: map[string]any{":token": got.msg.Delivery},
	})
	if err != nil && !errors.Is(err, errConditionFailed) {
		slog.Warn("record the receipt of a message held until its run is due", "execution", got.execution, "error", err)
	}
}

func (e *Engine) releaseHeld(ctx context.Context, deployed deployedConsumer, receipt string) error {
	url, err := e.queueURL(ctx, deployed.queue)
	if err != nil {
		return err
	}
	_, err = e.cfg.Queues.ChangeMessageVisibility(ctx, &sqs.ChangeMessageVisibilityInput{
		QueueUrl:          aws.String(url),
		ReceiptHandle:     aws.String(receipt),
		VisibilityTimeout: 0,
	})
	return err
}

func (e *Engine) deferMessage(ctx context.Context, deployed deployedConsumer, got received, wait time.Duration) disposition {
	if deployed.fifo() {
		return e.holdInQueue(ctx, deployed, got, wait)
	}
	if err := e.enqueue(ctx, deployed, got.msg, wait); err != nil {
		slog.Warn("put a message back on its queue until it is due", "execution", got.execution, "error", err)
		return retained
	}
	return acknowledged
}

func (e *Engine) holdInQueue(ctx context.Context, deployed deployedConsumer, got received, wait time.Duration) disposition {
	if receives := receiveCount(got.record); receives >= queues.MaxReceiveCount {
		return e.failUndelivered(ctx, deployed, got, receives)
	}
	url, err := e.queueURL(ctx, deployed.queue)
	if err == nil {
		_, err = e.cfg.Queues.ChangeMessageVisibility(ctx, &sqs.ChangeMessageVisibilityInput{
			QueueUrl:          aws.String(url),
			ReceiptHandle:     aws.String(got.record.ReceiptHandle),
			VisibilityTimeout: delaySeconds(max(wait, time.Second), maxVisibilityDelay),
		})
	}
	if err != nil {
		slog.Warn("hold a message on its queue", "queue", deployed.queue, "error", err)
	}
	return retained
}

func receiveCount(record events.SQSMessage) int {
	count, err := strconv.Atoi(record.Attributes[string(sqstypes.MessageSystemAttributeNameApproximateReceiveCount)])
	if err != nil {
		return 0
	}
	return count
}

func slotHold(receives int) time.Duration {
	hold := slotWait + time.Duration(rand.Int64N(int64(slotWait)))
	return min(max(hold, time.Duration(receives)*slotWait), maxSlotHold)
}

func (e *Engine) failUndelivered(ctx context.Context, deployed deployedConsumer, got received, receives int) disposition {
	now := time.Now()
	set := map[string]any{
		"status":      string(provider.RunFailed),
		"error":       fmt.Sprintf("its message was delivered %d times without the run starting, the most its queue allows before dead-lettering it", receives),
		"finished_at": now.UnixMicro(),
		"expires_at":  now.Add(runRetention).Unix(),
		"lease_until": 0,
	}
	if got.run.Payload == "" && len(got.msg.Payload) > 0 {
		set["payload"] = string(got.msg.Payload)
	}
	_, err := e.store.updateRun(context.WithoutCancel(ctx), deployed.topicName, got.execution, change{
		set:        set,
		condition:  "#status IN (:queued, :delayed) AND #delivery = :token",
		conditions: map[string]any{":queued": string(provider.RunQueued), ":delayed": string(provider.RunDelayed), ":token": got.msg.Delivery},
	})
	if err != nil {
		if !errors.Is(err, errConditionFailed) {
			slog.Warn("fail a run its queue is about to dead-letter", "execution", got.execution, "error", err)
		}
		return retained
	}
	return acknowledged
}

func (e *Engine) slotSets(deployed deployedConsumer) []slotSet {
	var sets []slotSet
	if deployed.consumer.Concurrency == 1 {
		sets = append(sets, slotSet{name: "consumer#" + deployed.topicName + "#" + deployed.consumer.Name, limit: 1})
	}
	if worker := e.cfg.Manifest.Workers[deployed.consumer.Worker]; worker.Concurrency > 0 {
		sets = append(sets, slotSet{name: "worker#" + deployed.consumer.Worker, limit: worker.Concurrency})
	}
	return sets
}

func (e *Engine) takeSlots(ctx context.Context, deployed deployedConsumer, holder string, until time.Time) ([]takenSlot, bool, error) {
	var taken []takenSlot
	for _, set := range e.slotSets(deployed) {
		index, ok, err := e.store.takeSlot(ctx, set, holder, until)
		if err != nil || !ok {
			e.releaseSlots(ctx, taken, holder)
			return nil, false, err
		}
		taken = append(taken, takenSlot{set: set, index: index})
	}
	return taken, true, nil
}

func (e *Engine) releaseSlots(ctx context.Context, taken []takenSlot, holder string) {
	for _, slot := range taken {
		if err := e.store.releaseSlot(context.WithoutCancel(ctx), slot.set, slot.index, holder); err != nil {
			slog.Warn("release a slot", "set", slot.set.name, "error", err)
		}
	}
}

func invocationDeadline(ctx context.Context) time.Time {
	if deadline, ok := ctx.Deadline(); ok {
		return deadline
	}
	return time.Now().Add(15 * time.Minute)
}

func (e *Engine) claim(ctx context.Context, deployed deployedConsumer, got received) (runItem, bool, error) {
	now := time.Now()
	item, err := e.store.updateRun(ctx, deployed.topicName, got.execution, change{
		set: map[string]any{
			"status":      string(provider.RunExecuting),
			"error":       "",
			"lease_until": invocationDeadline(ctx).Add(leaseMargin).UnixMicro(),
		},
		add:       map[string]int{"attempts": 1},
		condition: "#delivery = :token AND (#status IN (:queued, :delayed) OR (#status = :executing AND #lease_until < :now))",
		conditions: map[string]any{
			":token":     got.msg.Delivery,
			":queued":    string(provider.RunQueued),
			":delayed":   string(provider.RunDelayed),
			":executing": string(provider.RunExecuting),
			":now":       now.UnixMicro(),
		},
	})
	if errors.Is(err, errConditionFailed) {
		return runItem{}, false, nil
	}
	if err != nil {
		return runItem{}, false, err
	}
	if item.StartedAtMicros == 0 {
		started, err := e.store.updateRun(ctx, deployed.topicName, got.execution, change{
			set:        map[string]any{"started_at": now.UnixMicro()},
			condition:  "attribute_not_exists(started_at)",
			conditions: map[string]any{},
		})
		if err == nil {
			item = started
		} else if !errors.Is(err, errConditionFailed) {
			return runItem{}, false, err
		}
	}
	return item, true, nil
}

func (e *Engine) deliverOne(ctx context.Context, deployed deployedConsumer, record events.SQSMessage) disposition {
	got, err := e.recordArrival(ctx, deployed, record)
	if errors.Is(err, errRunGone) {
		return acknowledged
	}
	if err != nil {
		slog.Warn("read a delivered message", "queue", deployed.queue, "error", err)
		return retained
	}
	if ready, disposition := e.setAsideUnlessRunnable(ctx, deployed, got); !ready {
		return disposition
	}
	slots, taken, err := e.takeSlots(ctx, deployed, got.execution, invocationDeadline(ctx).Add(leaseMargin))
	if err != nil {
		slog.Warn("take a concurrency slot", "execution", got.execution, "error", err)
		return retained
	}
	if !taken {
		return e.deferMessage(ctx, deployed, got, slotHold(receiveCount(got.record)))
	}
	defer e.releaseSlots(ctx, slots, got.execution)
	for {
		run, claimed, err := e.claim(ctx, deployed, got)
		if err != nil {
			slog.Warn("claim a run", "execution", got.execution, "error", err)
			return retained
		}
		if !claimed {
			return acknowledged
		}
		got.run = run
		answer := e.attempt(ctx, deployed, []received{got})
		next, wait := e.finish(ctx, deployed, got, answer)
		if next != retryInPlace {
			return dispositionOf(next)
		}
		if remaining := time.Until(invocationDeadline(ctx)) - settleMargin; wait > inPlaceRetryCap || wait+attemptBudget(deployed) > remaining {
			return e.holdInQueue(ctx, deployed, got, wait)
		}
		select {
		case <-ctx.Done():
			return retained
		case <-time.After(wait):
		}
	}
}

func attemptBudget(deployed deployedConsumer) time.Duration {
	return deployed.consumer.MaxDuration
}

type settled int

const (
	done settled = iota
	keptInQueue
	retryInPlace
)

func dispositionOf(s settled) disposition {
	if s == keptInQueue {
		return retained
	}
	return acknowledged
}

func (e *Engine) envelopeOf(deployed deployedConsumer, claimed []received) *topicv1.Envelope {
	envelope := &topicv1.Envelope{
		V:        envelope.Version,
		Topic:    deployed.topicName,
		Consumer: deployed.consumer.Name,
		Schema:   envelope.SchemaOf(deployed.topic.Schema),
	}
	deliveries := make([]*topicv1.Delivery, 0, len(claimed))
	for _, got := range claimed {
		payload := json.RawMessage(got.run.Payload)
		if len(payload) == 0 {
			payload = got.msg.Payload
		}
		deliveries = append(deliveries, &topicv1.Delivery{
			Execution: got.execution,
			Message:   &topicv1.Message{Id: got.run.Message, PublishedAt: runs.TimestampOf(timeOfMicros(got.run.PublishedAtMicros))},
			Attempt:   &topicv1.Attempt{Number: int32(got.run.Attempts), Of: int32(got.run.MaxAttempts), FirstAttemptedAt: runs.TimestampOf(timeOfMicros(got.run.StartedAtMicros))},
			Payload:   payload,
		})
	}
	if deployed.consumer.Batch != nil && deployed.consumer.Batch.Size > 0 {
		envelope.Messages = deliveries
		return envelope
	}
	only := deliveries[0]
	envelope.Execution = only.GetExecution()
	envelope.Message = only.GetMessage()
	envelope.Attempt = only.GetAttempt()
	envelope.Payload = only.GetPayload()
	return envelope
}

func (e *Engine) attempt(ctx context.Context, deployed deployedConsumer, claimed []received) envelope.Result {
	postCtx, stopPost := context.WithDeadline(ctx, invocationDeadline(ctx).Add(-settleMargin))
	defer stopPost()
	attemptCtx, cancel := context.WithCancelCause(postCtx)
	defer cancel(nil)
	if maxDuration := deployed.consumer.MaxDuration; maxDuration > 0 {
		var stop context.CancelFunc
		attemptCtx, stop = context.WithTimeoutCause(attemptCtx, maxDuration, envelope.ErrTimedOut)
		defer stop()
	}
	if len(claimed) == 1 {
		watching, stopWatching := context.WithCancel(attemptCtx)
		defer stopWatching()
		go e.watchForCancel(watching, deployed, claimed[0], cancel)
	}
	return envelope.Post(postCtx, attemptCtx, e.cfg.Client, e.cfg.WorkerURL, e.envelopeOf(deployed, claimed))
}

func (e *Engine) watchForCancel(ctx context.Context, deployed deployedConsumer, got received, cancel context.CancelCauseFunc) {
	ticker := time.NewTicker(cancelPoll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		item, found, err := e.store.readRun(ctx, deployed.topicName, got.execution, false)
		if err != nil {
			continue
		}
		if !found || provider.RunStatus(item.Status) == provider.RunCanceled || item.Delivery != got.msg.Delivery {
			cancel(envelope.ErrCanceled)
			return
		}
	}
}

func (e *Engine) finish(ctx context.Context, deployed deployedConsumer, got received, answer envelope.Result) (settled, time.Duration) {
	ctx = context.WithoutCancel(ctx)
	switch answer.Outcome {
	case envelope.Canceled:
		return done, 0
	case envelope.Interrupted:
		return keptInQueue, 0
	case envelope.Succeeded:
		e.settle(ctx, deployed, got, provider.RunCompleted, answer.Output, "")
		return done, 0
	case envelope.Aborted, envelope.Refused:
		e.settle(ctx, deployed, got, provider.RunFailed, nil, answer.Reason)
		return done, 0
	case envelope.TimedOut:
		if deployed.isTask() {
			e.settle(ctx, deployed, got, provider.RunTimedOut, nil, answer.Reason)
			return done, 0
		}
	}
	if got.run.Attempts >= got.run.MaxAttempts {
		e.settle(ctx, deployed, got, provider.RunFailed, nil, answer.Reason)
		return done, 0
	}
	backoff := runs.Backoff(deployed.retry(), got.run.Attempts, runs.Jitter())
	if deployed.fifo() {
		if _, err := e.store.updateRun(ctx, deployed.topicName, got.execution, change{
			set:        map[string]any{"status": string(provider.RunQueued), "error": answer.Reason, "lease_until": 0},
			condition:  "#status = :executing AND #delivery = :token",
			conditions: map[string]any{":executing": string(provider.RunExecuting), ":token": got.msg.Delivery},
		}); err != nil {
			return dispositionOnFailure(err), 0
		}
		return retryInPlace, backoff
	}
	token := envelope.NewMessageID(time.Now())
	if _, err := e.store.updateRun(ctx, deployed.topicName, got.execution, change{
		set:        map[string]any{"status": string(provider.RunQueued), "error": answer.Reason, "delivery": token, "lease_until": 0},
		condition:  "#status = :executing AND #delivery = :token",
		conditions: map[string]any{":executing": string(provider.RunExecuting), ":token": got.msg.Delivery},
	}); err != nil {
		return dispositionOnFailure(err), 0
	}
	retry := got.msg
	retry.Execution, retry.Delivery = got.execution, token
	if err := e.enqueue(ctx, deployed, retry, backoff); err != nil {
		slog.Warn("queue a run's next attempt", "execution", got.execution, "error", err)
		_, _ = e.store.updateRun(ctx, deployed.topicName, got.execution, change{
			set:        map[string]any{"delivery": got.msg.Delivery},
			condition:  "#delivery = :token",
			conditions: map[string]any{":token": token},
		})
		return keptInQueue, 0
	}
	return done, 0
}

func dispositionOnFailure(err error) settled {
	if errors.Is(err, errConditionFailed) {
		return done
	}
	slog.Warn("record an attempt's outcome", "error", err)
	return keptInQueue
}

func (e *Engine) settle(ctx context.Context, deployed deployedConsumer, got received, status provider.RunStatus, output json.RawMessage, reason string) {
	now := time.Now()
	set := map[string]any{
		"status":      string(status),
		"error":       reason,
		"finished_at": now.UnixMicro(),
		"expires_at":  now.Add(runRetention).Unix(),
		"lease_until": 0,
	}
	if len(output) > 0 {
		set["output"] = string(output)
	}
	if status != provider.RunCompleted && got.run.Payload == "" && len(got.msg.Payload) > 0 {
		set["payload"] = string(got.msg.Payload)
	}
	if _, err := e.store.updateRun(ctx, deployed.topicName, got.execution, change{
		set:        set,
		condition:  "#status = :executing AND #delivery = :token",
		conditions: map[string]any{":executing": string(provider.RunExecuting), ":token": got.msg.Delivery},
	}); err != nil && !errors.Is(err, errConditionFailed) {
		slog.Warn("settle a run", "execution", got.execution, "error", err)
	}
}

func (e *Engine) deliverBatch(ctx context.Context, deployed deployedConsumer, records []events.SQSMessage) []string {
	var retainedIDs []string
	var admitted []received
	for i, record := range records {
		got, err := e.recordArrival(ctx, deployed, record)
		if errors.Is(err, errRunGone) {
			continue
		}
		keep := err != nil
		if err != nil {
			slog.Warn("read a delivered message", "queue", deployed.queue, "error", err)
		} else if ready, disposition := e.setAsideUnlessRunnable(ctx, deployed, got); !ready {
			keep = disposition == retained
		} else {
			admitted = append(admitted, got)
			continue
		}
		if !keep {
			continue
		}
		retainedIDs = append(retainedIDs, record.MessageId)
		if deployed.fifo() {
			for _, rest := range records[i+1:] {
				retainedIDs = append(retainedIDs, rest.MessageId)
			}
			break
		}
	}
	if len(admitted) == 0 {
		return retainedIDs
	}
	holder := admitted[0].execution
	slots, taken, err := e.takeSlots(ctx, deployed, holder, invocationDeadline(ctx).Add(leaseMargin))
	if err != nil || !taken {
		for _, got := range admitted {
			if e.deferMessage(ctx, deployed, got, slotHold(receiveCount(got.record))) == retained {
				retainedIDs = append(retainedIDs, got.record.MessageId)
			}
		}
		return retainedIDs
	}
	defer e.releaseSlots(ctx, slots, holder)
	var claimed []received
	for i, got := range admitted {
		run, ok, err := e.claim(ctx, deployed, got)
		if err != nil {
			slog.Warn("claim a run", "execution", got.execution, "error", err)
			retainedIDs = append(retainedIDs, got.record.MessageId)
			if deployed.fifo() {
				for _, rest := range admitted[i+1:] {
					retainedIDs = append(retainedIDs, rest.record.MessageId)
				}
				break
			}
			continue
		}
		if ok {
			got.run = run
			claimed = append(claimed, got)
		}
	}
	if len(claimed) == 0 {
		return retainedIDs
	}
	answer := e.attempt(ctx, deployed, claimed)
	for _, got := range claimed {
		next, wait := e.finish(ctx, deployed, got, answer)
		retain := dispositionOf(next) == retained
		if next == retryInPlace {
			retain = e.holdInQueue(ctx, deployed, got, wait) == retained
		}
		if retain {
			retainedIDs = append(retainedIDs, got.record.MessageId)
		}
	}
	return retainedIDs
}

func (e *Engine) FireCron(ctx context.Context, task string, scheduled time.Time) error {
	deployed, found := e.taskConsumer(task)
	if !found {
		return fmt.Errorf("no task named %q is deployed to run on its schedule", task)
	}
	stamp := scheduled.UTC().Truncate(time.Minute).Format(time.RFC3339)
	payload, err := json.Marshal(map[string]string{"timestamp": stamp})
	if err != nil {
		return err
	}
	_, err = e.Tasks().trigger(ctx, deployed, payload, &taskv1.TriggerOptions{IdempotencyKey: "cron-" + stamp})
	return err
}

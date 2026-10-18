package tasks

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-lambda-go/events"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/ocelhq/ocel/pkg/runs"
)

const (
	settleMargin        = 3 * time.Second
	longestBatchingWait = 5 * time.Minute
)

func queueOfARN(arn string) string {
	return arn[strings.LastIndex(arn, ":")+1:]
}

func receiveCount(attributes map[string]string) int {
	count, err := strconv.Atoi(attributes[string(sqstypes.MessageSystemAttributeNameApproximateReceiveCount)])
	if err != nil {
		return 0
	}
	return count
}

func (e *Engine) Deliver(ctx context.Context, event events.SQSEvent) events.SQSEventResponse {
	receivedAt := time.Now().Add(-longestBatchingWait)
	var resp events.SQSEventResponse
	keep := func(record events.SQSMessage) {
		resp.BatchItemFailures = append(resp.BatchItemFailures, events.SQSBatchItemFailure{ItemIdentifier: record.MessageId})
	}
	byQueue := map[string][]events.SQSMessage{}
	var order []string
	for _, record := range event.Records {
		if marker, scheduled := readCronMarker(record.Body); scheduled {
			if err := e.FireCron(ctx, marker.Cron, marker.scheduledAt()); err != nil {
				slog.Warn("start a task on its schedule", "task", marker.Cron, "error", err)
				keep(record)
			}
			continue
		}
		queue := queueOfARN(record.EventSourceARN)
		if _, seen := byQueue[queue]; !seen {
			order = append(order, queue)
		}
		byQueue[queue] = append(byQueue[queue], record)
	}
	attempts := ctx
	if deadline, bounded := ctx.Deadline(); bounded {
		var cancel context.CancelFunc
		attempts, cancel = context.WithDeadline(ctx, deadline.Add(-settleMargin))
		defer cancel()
	}
	for _, queue := range order {
		records := byQueue[queue]
		topic, consumer, found := e.queue.consumerOnQueue(queue)
		if !found {
			slog.Warn("a message arrived from a queue this deployment does not consume", "queue", queue)
			for _, record := range records {
				keep(record)
			}
			continue
		}
		var received []runs.Received
		var delivered []events.SQSMessage
		for _, record := range records {
			msg, err := messageOf(topic, consumer, record.Body)
			if err != nil {
				slog.Warn("read a delivered message", "queue", queue, "message", record.MessageId, "error", err)
				keep(record)
				continue
			}
			received = append(received, runs.Received{Handle: record.ReceiptHandle, Message: msg, Receives: receiveCount(record.Attributes)})
			delivered = append(delivered, record)
		}
		if len(received) == 0 {
			continue
		}
		dispositions := e.runs.Deliver(attempts, topic, consumer, received)
		url, err := e.queue.url(ctx, queue)
		if err != nil {
			slog.Warn("hide the messages a delivery gave back", "queue", queue, "error", err)
		}
		settling := context.WithoutCancel(ctx)
		now := time.Now()
		var changes []visibilityChange
		for i, disposition := range dispositions {
			if disposition.Done {
				continue
			}
			record := delivered[i]
			if url != "" && outlivesRetention(queue, sentAtOf(record.Attributes), disposition.After, now) {
				if err := e.queue.sendAgain(settling, url, record.Body, disposition.After); err == nil {
					continue
				}
				slog.Warn("send a message again before its queue drops it", "queue", queue, "message", record.MessageId, "error", err)
			}
			keep(record)
			if change, changeable := visibilityOf(record.ReceiptHandle, disposition.After, receivedAt, now); changeable {
				changes = append(changes, change)
			}
		}
		if url != "" && len(changes) > 0 {
			e.queue.changeVisibility(settling, url, changes)
		}
	}
	return resp
}

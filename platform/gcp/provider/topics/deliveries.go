package topics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/ocelhq/ocel/pkg/envelope"
	topicv1 "github.com/ocelhq/ocel/pkg/proto/app/topic/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

const (
	MessageAttribute     = "ocel-message"
	PublishedAtAttribute = "ocel-published-at"
	DueAtAttribute       = "ocel-due-at"
	DelayTaskAttribute   = "ocel-delay-task"
	MaxAttemptsAttribute = "ocel-max-attempts"
	LaneAttribute        = "ocel-lane"
	ScheduleAttribute    = "ocel-schedule"

	pushPrefix    = "/topics/"
	consumerInfix = "/consumers/"
)

type Deliveries struct {
	Store  Store
	Topics map[string]*contractv1.ManifestTopic
	Worker string
}

type pushRequest struct {
	Message struct {
		Data        []byte            `json:"data"`
		Attributes  map[string]string `json:"attributes"`
		MessageID   string            `json:"messageId"`
		PublishTime string            `json:"publishTime"`
		OrderingKey string            `json:"orderingKey"`
	} `json:"message"`
}

type pushedRun struct {
	topicName   string
	topic       *contractv1.ManifestTopic
	consumer    *contractv1.ManifestConsumer
	messageID   string
	publishedAt time.Time
	dueAt       time.Time
	payload     json.RawMessage
	key         string
	lane        string
	maxAttempts int
	delayTask   string
}

func (d Deliveries) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	topicName, consumerName, routed := pushedTo(r)
	topic, consumer, deployed := d.consumerOf(topicName, consumerName)
	if !routed || !deployed {
		http.Error(w, fmt.Sprintf("no consumer %q of topic %q is deployed on this worker", consumerName, topicName), http.StatusNotFound)
		return
	}
	var pushed pushRequest
	if err := json.NewDecoder(r.Body).Decode(&pushed); err != nil {
		http.Error(w, fmt.Sprintf("the push is not a Pub/Sub message: %v", err), http.StatusBadRequest)
		return
	}
	delivered := pushedRunOf(topicName, topic, consumer, pushed)
	if err := d.deliver(r.Context(), delivered); err != nil {
		if r.Context().Err() == nil {
			slog.Warn("deliver a message", "topic", topicName, "consumer", consumerName, "message", delivered.messageID, "error", err)
		}
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func pushedTo(r *http.Request) (string, string, bool) {
	if r.Method != http.MethodPost {
		return "", "", false
	}
	rest, found := strings.CutPrefix(r.URL.Path, pushPrefix)
	if !found {
		return "", "", false
	}
	topic, consumer, found := strings.Cut(rest, consumerInfix)
	return topic, consumer, found && topic != "" && consumer != "" && !strings.Contains(consumer, "/")
}

func (d Deliveries) consumerOf(topicName, consumerName string) (*contractv1.ManifestTopic, *contractv1.ManifestConsumer, bool) {
	topic, found := d.Topics[topicName]
	if !found {
		return nil, nil, false
	}
	for _, consumer := range topic.GetConsumers() {
		if consumer.GetName() == consumerName {
			return topic, consumer, true
		}
	}
	return nil, nil, false
}

func pushedRunOf(topicName string, topic *contractv1.ManifestTopic, consumer *contractv1.ManifestConsumer, pushed pushRequest) pushedRun {
	attributes := pushed.Message.Attributes
	publishTime, err := time.Parse(time.RFC3339Nano, pushed.Message.PublishTime)
	idTime := publishTime
	if err != nil {
		idTime, publishTime = time.Unix(0, 0), time.Now()
	}
	delivered := pushedRun{
		topicName:   topicName,
		topic:       topic,
		consumer:    consumer,
		messageID:   attributes[MessageAttribute],
		publishedAt: publishTime,
		payload:     pushed.Message.Data,
		key:         pushed.Message.OrderingKey,
		lane:        attributes[LaneAttribute],
		delayTask:   attributes[DelayTaskAttribute],
	}
	if at, err := time.Parse(time.RFC3339Nano, attributes[PublishedAtAttribute]); err == nil {
		delivered.publishedAt = at
	}
	delivered.dueAt = delivered.publishedAt
	if at, err := time.Parse(time.RFC3339Nano, attributes[DueAtAttribute]); err == nil {
		delivered.dueAt = at
	}
	if _, scheduled := attributes[ScheduleAttribute]; scheduled {
		delivered.payload = scheduledPayloadOf(publishTime)
	}
	if delivered.messageID == "" {
		delivered.messageID = envelope.MessageIDFrom(idTime, pushed.Message.MessageID)
	}
	requested, _ := strconv.Atoi(attributes[MaxAttemptsAttribute])
	delivered.maxAttempts = retryPolicyOf(topic, consumer).attemptsFor(int32(requested))
	return delivered
}

func (p pushedRun) execution() string { return executionOf(p.messageID, p.consumer.GetName()) }

func executionOf(messageID, consumer string) string { return messageID + "-" + consumer }

func isFinished(status provider.RunStatus) bool {
	switch status {
	case provider.RunCompleted, provider.RunFailed, provider.RunCanceled, provider.RunExpired, provider.RunTimedOut:
		return true
	}
	return false
}

func (d Deliveries) deliver(ctx context.Context, delivered pushedRun) error {
	claimed, err := d.claim(ctx, delivered)
	if errors.Is(err, errRunUnchanged) {
		return nil
	}
	if err != nil {
		return err
	}
	if claimed.Status != provider.RunExecuting {
		return nil
	}
	attemptCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	if maxDuration := delivered.consumer.GetMaxDuration().AsDuration(); maxDuration > 0 {
		var stop context.CancelFunc
		attemptCtx, stop = context.WithTimeoutCause(attemptCtx, maxDuration, envelope.ErrTimedOut)
		defer stop()
	}
	res := envelope.Post(ctx, attemptCtx, http.DefaultClient, d.Worker, envelopeOf(delivered, claimed))
	retry, err := d.finish(ctx, delivered, res)
	if err != nil && !errors.Is(err, errRunUnchanged) {
		return err
	}
	if retry {
		return fmt.Errorf("attempt %d of %s failed and is retried: %s", claimed.Attempts, delivered.execution(), res.Reason)
	}
	return nil
}

func (d Deliveries) claim(ctx context.Context, delivered pushedRun) (storedRun, error) {
	return d.Store.changeRun(ctx, delivered.execution(), func(run *storedRun, found bool) error {
		now := time.Now()
		if !found {
			startStoredRun(run, delivered)
		}
		if run.delivery.MaxAttempts == 0 {
			run.delivery.MaxAttempts = delivered.maxAttempts
		}
		if isFinished(run.Status) || isSuperseded(*run, delivered) {
			return errRunUnchanged
		}
		if run.Attempts == 0 && !run.ExpiresAt.IsZero() && !run.ExpiresAt.After(now) {
			run.Status, run.FinishedAt = provider.RunExpired, now
			return nil
		}
		run.Status, run.Error = provider.RunExecuting, ""
		run.Attempts++
		if run.StartedAt.IsZero() {
			run.StartedAt = now
		}
		return nil
	})
}

func isSuperseded(run storedRun, delivered pushedRun) bool {
	return delivered.delayTask != "" && delivered.delayTask != run.delivery.DelayTask
}

func startStoredRun(run *storedRun, delivered pushedRun) {
	publishedAt := delivered.publishedAt
	run.Topic, run.Consumer = delivered.topicName, delivered.consumer.GetName()
	run.Status, run.CreatedAt, run.DueAt = provider.RunQueued, publishedAt, delivered.dueAt
	run.delivery = deliveryFields{
		MessageID:   delivered.messageID,
		PublishedAt: &publishedAt,
		MaxAttempts: delivered.maxAttempts,
		Key:         delivered.key,
		Lane:        delivered.lane,
		DelayTask:   delivered.delayTask,
	}
	if ttl := delivered.topic.GetTtl().AsDuration(); ttl > 0 {
		run.ExpiresAt = delivered.dueAt.Add(ttl)
	}
	if isTask(delivered.topic) {
		run.Payload = delivered.payload
	}
}

func envelopeOf(delivered pushedRun, claimed storedRun) *topicv1.Envelope {
	message := &topicv1.Message{Id: delivered.messageID, PublishedAt: timestamppb.New(delivered.publishedAt)}
	attempt := &topicv1.Attempt{
		Number:           int32(claimed.Attempts),
		Of:               int32(claimed.delivery.MaxAttempts),
		FirstAttemptedAt: timestamppb.New(claimed.StartedAt),
	}
	posted := &topicv1.Envelope{
		V:        envelope.Version,
		Topic:    delivered.topicName,
		Consumer: delivered.consumer.GetName(),
		Schema:   envelope.SchemaOf(delivered.topic.GetSchema()),
	}
	if delivered.consumer.GetBatch().GetSize() > 0 {
		posted.Messages = []*topicv1.Delivery{{Execution: delivered.execution(), Message: message, Attempt: attempt, Payload: delivered.payload}}
		return posted
	}
	posted.Execution, posted.Message, posted.Attempt, posted.Payload = delivered.execution(), message, attempt, delivered.payload
	return posted
}

func (d Deliveries) finish(ctx context.Context, delivered pushedRun, res envelope.Result) (bool, error) {
	retry := false
	_, err := d.Store.changeRun(ctx, delivered.execution(), func(run *storedRun, found bool) error {
		if !found || run.Status != provider.RunExecuting {
			return errRunUnchanged
		}
		now := time.Now()
		settle := func(status provider.RunStatus, output json.RawMessage, reason string) {
			run.Status, run.Output, run.Error, run.FinishedAt = status, output, reason, now
			if status != provider.RunCompleted && len(run.Payload) == 0 {
				run.Payload = delivered.payload
			}
		}
		switch res.Outcome {
		case envelope.Canceled, envelope.Interrupted:
			retry = true
			return errRunUnchanged
		case envelope.Succeeded:
			settle(provider.RunCompleted, res.Output, "")
			return nil
		case envelope.Aborted, envelope.Refused:
			settle(provider.RunFailed, nil, res.Reason)
			return nil
		case envelope.TimedOut:
			if isTask(delivered.topic) {
				settle(provider.RunTimedOut, nil, res.Reason)
				return nil
			}
		}
		if run.Attempts >= run.delivery.MaxAttempts {
			settle(provider.RunFailed, nil, res.Reason)
			return nil
		}
		run.Status, run.Error = provider.RunQueued, res.Reason
		retry = true
		return nil
	})
	return retry, err
}

package delivery

import (
	"context"
	"encoding/json"
	"io"
	"net/http"

	"google.golang.org/protobuf/encoding/protojson"
	"ocel.dev/internal/binding"
	topicv1 "ocel.dev/internal/proto/app/topic/v1"
	"ocel.dev/internal/runcontext"
	"ocel.dev/runs"
)

func Handler(name string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		deliver(r.Context(), name, r.Body).write(w)
	})
}

func deliver(ctx context.Context, name string, body io.Reader) answer {
	raw, err := io.ReadAll(body)
	if err != nil {
		return newRefusalAnswer(http.StatusBadRequest, "the delivery could not be read: %v", err)
	}
	envelope, err := decodeEnvelope(raw)
	if err != nil {
		return newRefusalAnswer(http.StatusBadRequest, "the delivery is not an envelope: %v", err)
	}
	r := findRoute(envelope.GetTopic(), envelope.GetConsumer())
	switch {
	case r == nil:
		return newRefusalAnswer(http.StatusNotFound, "this app declares no consumer %q of topic %q", envelope.GetConsumer(), envelope.GetTopic())
	case r.Worker != name:
		return newRefusalAnswer(http.StatusNotFound, "consumer %q of topic %q runs on worker %q, not on worker %q", envelope.GetConsumer(), envelope.GetTopic(), r.Worker, name)
	case !r.Batched && len(envelope.GetMessages()) > 0:
		return newRefusalAnswer(http.StatusBadRequest, "consumer %q of topic %q takes one message per delivery, and this one carries %d", envelope.GetConsumer(), envelope.GetTopic(), len(envelope.GetMessages()))
	}

	served := ensureWorker(name)
	release, err := served.acquireSlot(ctx)
	if err != nil {
		return newRetryAnswer(err)
	}
	defer release()
	if err := served.start(ctx); err != nil {
		return newRetryAnswer(err)
	}
	ctx = context.WithValue(ctx, runcontext.Key{}, newRunContext(r, envelope))
	return r.Serve(ctx, envelope, served.wrapAttempt)
}

func decodeEnvelope(raw []byte) (*topicv1.Envelope, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	payload := fields["payload"]
	delete(fields, "payload")
	var deliveries []map[string]json.RawMessage
	if batch, found := fields["messages"]; found {
		if err := json.Unmarshal(batch, &deliveries); err != nil {
			return nil, err
		}
	}
	payloads := make([]json.RawMessage, len(deliveries))
	for i, delivery := range deliveries {
		payloads[i] = delivery["payload"]
		delete(delivery, "payload")
	}
	if deliveries != nil {
		batch, err := json.Marshal(deliveries)
		if err != nil {
			return nil, err
		}
		fields["messages"] = batch
	}
	bare, err := json.Marshal(fields)
	if err != nil {
		return nil, err
	}
	envelope := &topicv1.Envelope{}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(bare, envelope); err != nil {
		return nil, err
	}
	envelope.Payload = payload
	for i, delivery := range envelope.GetMessages() {
		delivery.Payload = payloads[i]
	}
	return envelope, nil
}

func NewSingleServeFunc[P, R any](w Work[P, R]) ServeFunc {
	return func(ctx context.Context, envelope *topicv1.Envelope, wrap Middleware) answer {
		var payload P
		if err := binding.DecodePayload(envelope.GetPayload(), &payload); err != nil {
			return newAbortAnswer(err.Error())
		}
		return w.serve(ctx, payload, wrap)
	}
}

func NewBatchServeFunc[P, R any](w Work[[]P, R]) ServeFunc {
	return func(ctx context.Context, envelope *topicv1.Envelope, wrap Middleware) answer {
		raws := [][]byte{envelope.GetPayload()}
		if batch := envelope.GetMessages(); len(batch) > 0 {
			raws = raws[:0]
			for _, delivery := range batch {
				raws = append(raws, delivery.GetPayload())
			}
		}
		payloads := make([]P, len(raws))
		for i, raw := range raws {
			if err := binding.DecodePayload(raw, &payloads[i]); err != nil {
				return newAbortAnswer(err.Error())
			}
		}
		return w.serve(ctx, payloads, wrap)
	}
}

func newRunContext(r *Route, envelope *topicv1.Envelope) runs.Context {
	id, message, attempt := envelope.GetExecution(), envelope.GetMessage(), envelope.GetAttempt()
	if batch := envelope.GetMessages(); len(batch) > 0 {
		first := batch[0]
		id = first.GetExecution()
		if first.GetMessage() != nil {
			message = first.GetMessage()
		}
		if first.GetAttempt() != nil {
			attempt = first.GetAttempt()
		}
	}
	run := runs.Context{
		Kind:    r.Kind,
		Name:    r.Name,
		Topic:   envelope.GetTopic(),
		ID:      id,
		Attempt: runs.Attempt{Number: 1, Of: 1},
		Message: runs.Message{ID: message.GetId(), PublishedAt: binding.DecodeTimestamp(message.GetPublishedAt())},
	}
	if attempt != nil {
		run.Attempt = runs.Attempt{
			Number:           int(attempt.GetNumber()),
			Of:               int(attempt.GetOf()),
			FirstAttemptedAt: binding.DecodeTimestamp(attempt.GetFirstAttemptedAt()),
		}
	}
	return run
}

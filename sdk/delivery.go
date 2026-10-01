package ocel

import (
	"context"
	"encoding/json"
	"io"
	"net/http"

	"google.golang.org/protobuf/encoding/protojson"
	topicv1 "ocel.dev/internal/proto/app/topic/v1"
)

// WorkerHandler serves the deliveries Ocel makes to the worker named name: the
// tasks and consumers placed on it. Ocel's generated worker entry mounts it,
// so an app does not.
//
// It answers 200 with the run's output as JSON when the run succeeds, 422
// when it is aborted, and 500 when it is to be retried. A cancelled request
// cancels the context the run is handed and calls [OnCancel] in place of
// the other lifecycle hooks; the answer is still what the run did.
func WorkerHandler(name string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		deliver(r.Context(), name, r.Body).write(w)
	})
}

func deliver(ctx context.Context, worker string, body io.Reader) answer {
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
	case r.worker != worker:
		return newRefusalAnswer(http.StatusNotFound, "consumer %q of topic %q runs on worker %q, not on worker %q", envelope.GetConsumer(), envelope.GetTopic(), r.worker, worker)
	case !r.batched && len(envelope.GetMessages()) > 0:
		return newRefusalAnswer(http.StatusBadRequest, "consumer %q of topic %q takes one message per delivery, and this one carries %d", envelope.GetConsumer(), envelope.GetTopic(), len(envelope.GetMessages()))
	}

	served := ensureWorker(worker)
	release, err := served.acquireSlot(ctx)
	if err != nil {
		return newRetryAnswer(err)
	}
	defer release()
	if err := served.start(ctx); err != nil {
		return newRetryAnswer(err)
	}
	ctx = context.WithValue(ctx, runContextKey{}, newRunContext(r, envelope))
	return r.serve(ctx, envelope, served.wrapAttempt)
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

func newSingleServeFunc[P, R any](w work[P, R]) serveFunc {
	return func(ctx context.Context, envelope *topicv1.Envelope, wrap middleware) answer {
		var payload P
		if err := decodePayload(envelope.GetPayload(), &payload); err != nil {
			return newAbortAnswer(err.Error())
		}
		return w.serve(ctx, payload, wrap)
	}
}

func newBatchServeFunc[P, R any](w work[[]P, R]) serveFunc {
	return func(ctx context.Context, envelope *topicv1.Envelope, wrap middleware) answer {
		raws := [][]byte{envelope.GetPayload()}
		if batch := envelope.GetMessages(); len(batch) > 0 {
			raws = raws[:0]
			for _, delivery := range batch {
				raws = append(raws, delivery.GetPayload())
			}
		}
		payloads := make([]P, len(raws))
		for i, raw := range raws {
			if err := decodePayload(raw, &payloads[i]); err != nil {
				return newAbortAnswer(err.Error())
			}
		}
		return w.serve(ctx, payloads, wrap)
	}
}

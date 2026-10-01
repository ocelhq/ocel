package ocel

import (
	"context"
	"io"
	"net/http"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"
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
	envelope := &topicv1.Envelope{}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(raw, envelope); err != nil {
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

func newSingleServeFunc[P, R any](w work[P, R]) serveFunc {
	return func(ctx context.Context, envelope *topicv1.Envelope, wrap middleware) answer {
		var payload P
		if err := decodeValue(envelope.GetPayload(), &payload); err != nil {
			return newAbortAnswer(err.Error())
		}
		return w.serve(ctx, payload, wrap)
	}
}

func newBatchServeFunc[P, R any](w work[[]P, R]) serveFunc {
	return func(ctx context.Context, envelope *topicv1.Envelope, wrap middleware) answer {
		values := []*structpb.Value{envelope.GetPayload()}
		if batch := envelope.GetMessages(); len(batch) > 0 {
			values = values[:0]
			for _, delivery := range batch {
				values = append(values, delivery.GetPayload())
			}
		}
		payloads := make([]P, len(values))
		for i, value := range values {
			if err := decodeValue(value, &payloads[i]); err != nil {
				return newAbortAnswer(err.Error())
			}
		}
		return w.serve(ctx, payloads, wrap)
	}
}

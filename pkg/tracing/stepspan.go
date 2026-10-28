package tracing

import (
	"crypto/sha256"

	"go.opentelemetry.io/otel/trace"
)

func StepSpanID(call trace.SpanID, step []byte) trace.SpanID {
	digest := sha256.Sum256(append(call[:], step...))
	var id trace.SpanID
	copy(id[:], digest[:])
	if !id.IsValid() {
		id[len(id)-1] = 1
	}
	return id
}

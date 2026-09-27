package runtrace

import (
	"testing"
	"time"

	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

func TestAScopeStartedWithNoParentIsASpanUnderTheRunsRootSpan(t *testing.T) {
	r := startRun(t)
	id := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	now := time.Now()
	r.Receive(started(id, nil, "aws:s3:Bucket create"))
	r.Receive(ended(id, now, now.Add(time.Second), progressv1.SpanStatus_SPAN_STATUS_OK))
	if err := r.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	spans := readTraceDoc(t, r)
	ingested := spanNamed(t, spans, "aws:s3:Bucket create")

	var root *otlpTestSpan
	for i := range spans {
		if spans[i].ParentSpanId == "" {
			root = &spans[i]
		}
	}
	if root == nil {
		t.Fatal("no root span (a span with no parent) in the trace file")
	}
	if ingested.ParentSpanId != root.SpanId {
		t.Errorf("ingested span parentSpanId = %q, want the run's root span id %q", ingested.ParentSpanId, root.SpanId)
	}
}

package obs

import (
	"context"
	"testing"
	"time"
)

func TestIngestSpanWithNoParentAttachesToTheRunsRootSpan(t *testing.T) {
	dir := t.TempDir()
	_, r, err := Start(context.Background(), dir, "ocel deploy")
	if err != nil {
		t.Fatalf("Start() = %v", err)
	}

	spanID := [8]byte{1, 2, 3, 4, 5, 6, 7, 8}
	now := time.Now()
	r.IngestSpan(spanID, [8]byte{}, "aws:s3:Bucket create", now, now.Add(time.Second), SpanStatusOK, nil)

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

func TestIngestSpanWithAParentKeepsIt(t *testing.T) {
	dir := t.TempDir()
	_, r, err := Start(context.Background(), dir, "ocel deploy")
	if err != nil {
		t.Fatalf("Start() = %v", err)
	}

	parentID := [8]byte{9, 9, 9, 9, 9, 9, 9, 9}
	childID := [8]byte{1, 2, 3, 4, 5, 6, 7, 8}
	now := time.Now()
	r.IngestSpan(parentID, [8]byte{}, "stage", now, now.Add(time.Second), SpanStatusOK, nil)
	r.IngestSpan(childID, parentID, "resource", now, now.Add(time.Second), SpanStatusOK, nil)

	if err := r.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	spans := readTraceDoc(t, r)
	child := spanNamed(t, spans, "resource")
	parent := spanNamed(t, spans, "stage")
	if child.ParentSpanId != parent.SpanId {
		t.Errorf("child span parentSpanId = %q, want the explicit parent %q", child.ParentSpanId, parent.SpanId)
	}
}

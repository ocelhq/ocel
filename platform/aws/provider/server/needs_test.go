package server

import (
	"context"
	"testing"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func TestDeployReporterStreamsAWaivedNeedAsATypedEvent(t *testing.T) {
	t.Parallel()

	stream := &recordingStream{}
	sender := newEventSender(context.Background(), stream.send)
	_, _, _, degraded := newDeployReporter(sender, newDeployStages())

	degraded(edge.NeedEdgeMiddleware, "web: middleware runs in the origin's Node server the way `next start` runs it. It affects routes /dashboard")
	if err := sender.close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	events := stream.events
	if len(events) != 1 {
		t.Fatalf("got %d events, want the one degraded event", len(events))
	}
	d := events[0].GetDegraded()
	if d == nil {
		t.Fatalf("event = %v, want a DegradedEvent", events[0])
	}
	if d.GetNeed() != string(edge.NeedEdgeMiddleware) {
		t.Errorf("need = %q, want edge-middleware", d.GetNeed())
	}
	if d.GetDetail() == "" {
		t.Error("detail is empty, want the degrade spelled out")
	}
}

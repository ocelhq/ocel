package providerkit

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	connect "connectrpc.com/connect"

	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type recordingStream struct {
	mu     sync.Mutex
	events []*progressv1.OperationEvent
}

func (r *recordingStream) send(ev *progressv1.OperationEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
	return nil
}

func (r *recordingStream) recorded() []*progressv1.OperationEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.events
}

func TestEventSenderPropagatesSendError(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("boom")
	var calls int
	sender := newEventSender(context.Background(), func(*progressv1.OperationEvent) error {
		calls++
		if calls == 2 {
			return wantErr
		}
		return nil
	})

	const total = 5
	for range total {
		sender.send(logEvent("line"))
	}

	if err := sender.close(); !errors.Is(err, wantErr) {
		t.Fatalf("close() error = %v, want %v", err, wantErr)
	}
	if calls != total {
		t.Fatalf("send attempted for %d events, want %d: a mid-stream error must not skip the rest, including the terminal result", calls, total)
	}
}

func TestEventSenderAppliesBackpressureWithoutDroppingEvents(t *testing.T) {
	t.Parallel()

	stream := &recordingStream{}
	sender := newEventSender(context.Background(), stream.send)

	const total = eventSenderBuffer + 50
	var wg sync.WaitGroup
	for range total {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sender.send(logEvent("line"))
		}()
	}
	wg.Wait()

	if err := sender.close(); err != nil {
		t.Fatalf("close() error = %v", err)
	}
	if got := len(stream.recorded()); got != total {
		t.Fatalf("got %d events, want %d", got, total)
	}
}

func TestEventSenderSendRacingCloseNeverPanics(t *testing.T) {
	t.Parallel()

	stream := &recordingStream{}
	sender := newEventSender(context.Background(), stream.send)

	const concurrent = 50
	var wg sync.WaitGroup
	for range concurrent {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sender.send(logEvent("racing close"))
		}()
	}

	closeDone := make(chan struct{})
	go func() {
		defer close(closeDone)
		sender.close()
	}()

	wg.Wait()
	<-closeDone

	for range concurrent {
		sender.send(logEvent("after close"))
	}
}

func TestEventSenderSendUnblocksOnContextCancellation(t *testing.T) {
	t.Parallel()

	blockDrain := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())

	sender := newEventSender(ctx, func(*progressv1.OperationEvent) error {
		<-blockDrain
		return nil
	})

	var fillers sync.WaitGroup
	for range eventSenderBuffer + 1 {
		fillers.Add(1)
		go func() {
			defer fillers.Done()
			sender.send(logEvent("line"))
		}()
	}
	fillers.Wait()

	cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		sender.send(logEvent("blocked"))
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("send did not observe context cancellation once the buffer was saturated")
	}

	close(blockDrain)
	sender.close()
}

func TestEventSenderFailPassesARefusalBackToTheCaller(t *testing.T) {
	t.Parallel()

	stream := &recordingStream{}
	sender := newEventSender(context.Background(), stream.send)

	refusal := connect.NewError(connect.CodeInvalidArgument, errors.New("no"))
	if err := sender.fail(refusal); !errors.Is(err, refusal) {
		t.Fatalf("fail(refusal) = %v, want the refusal returned so the RPC carries the code", err)
	}
	if err := sender.fail(errors.New("the engine gave up")); err != nil {
		t.Fatalf("fail(failure) = %v, want nil so the failure travels as a result event", err)
	}

	if err := sender.close(); err != nil {
		t.Fatalf("close() error = %v", err)
	}
	events := stream.recorded()
	if len(events) != 1 {
		t.Fatalf("got %d events, want only the failure result", len(events))
	}
	if result := events[0].GetResult(); result.GetSuccess() || result.GetError() != "the engine gave up" {
		t.Fatalf("result = %+v, want the failure carried as an unsuccessful result", result)
	}
}

func TestReporterTagsEverythingWithItsStage(t *testing.T) {
	t.Parallel()

	stream := &recordingStream{}
	sender := newEventSender(context.Background(), stream.send)
	stage := NewRootStage("Provisioning")
	report := newReporter(sender, stage, progressv1.Phase_PHASE_PROVISIONING)

	report.Say("provisioning the infra stack")
	report.Detail("engine said something")
	report.Span("infra", time.Unix(1000, 0), time.Unix(1005, 0), nil)

	if err := sender.close(); err != nil {
		t.Fatalf("close() error = %v", err)
	}
	events := stream.recorded()
	if len(events) != 3 {
		t.Fatalf("got %d events, want 3", len(events))
	}

	said := events[0].GetProgress()
	if StageID(said.GetStageId()) != stage.ID {
		t.Errorf("Say() StageId = %x, want %x", said.GetStageId(), stage.ID)
	}
	if said.GetPhase() != progressv1.Phase_PHASE_PROVISIONING {
		t.Errorf("Say() Phase = %v, want the reporter's phase", said.GetPhase())
	}
	if events[1].GetLog().GetMessage() != "engine said something" {
		t.Errorf("Detail() log = %q", events[1].GetLog().GetMessage())
	}
	if span := events[2].GetSpan(); StageID(span.GetParentSpanId()) != stage.ID {
		t.Errorf("Span() ParentSpanId = %x, want the reporter's stage %x", span.GetParentSpanId(), stage.ID)
	}
}

func TestReporterStripsControlCharacters(t *testing.T) {
	t.Parallel()

	stream := &recordingStream{}
	sender := newEventSender(context.Background(), stream.send)
	report := newReporter(sender, NewRootStage("Provisioning"), progressv1.Phase_PHASE_PROVISIONING)

	report.Say("clearing the screen\x1b[2J now")

	if err := sender.close(); err != nil {
		t.Fatalf("close() error = %v", err)
	}
	if got := stream.recorded()[0].GetProgress().GetMessage(); got != "clearing the screen[2J now" {
		t.Errorf("Say() message = %q, want the control characters gone", got)
	}
}

func TestEventConstructors(t *testing.T) {
	t.Parallel()

	stage := NewRootStage("Provisioning")

	if got := progressEvent("plain").GetProgress().GetMessage(); got != "plain" {
		t.Errorf("progressEvent() message = %q", got)
	}

	if got := stageProgressEvent(stage.ID, progressv1.Phase_PHASE_DELETING, "deleting").GetProgress(); StageID(got.GetStageId()) != stage.ID {
		t.Errorf("stageProgressEvent() StageId = %x, want %x", got.GetStageId(), stage.ID)
	}

	degraded := degradedEvent(edge.NeedEdgeMiddleware, "the edge cannot run code").GetDegraded()
	if degraded.GetNeed() != string(edge.NeedEdgeMiddleware) {
		t.Errorf("degradedEvent() Need = %q", degraded.GetNeed())
	}

	owed := dnsOwedEvent("add these", []edge.Record{{Name: "app.example.com", Type: edge.RecordTypeCNAME, Value: "front"}}, "note").GetDnsOwed()
	if len(owed.GetRecords()) != 1 || owed.GetRecords()[0].GetName() != "app.example.com" {
		t.Errorf("dnsOwedEvent() records = %+v", owed.GetRecords())
	}

	if !okResult().GetResult().GetSuccess() {
		t.Error("okResult() is not a success")
	}
}

func TestFailStreamPassesARefusalThroughUnsent(t *testing.T) {
	t.Parallel()

	refusal := connect.NewError(connect.CodeInvalidArgument, errors.New("no"))
	if !refusedRequest(refusal) {
		t.Fatal("refusedRequest() = false for an InvalidArgument error")
	}
	if err := failStream(nil, refusal); !errors.Is(err, refusal) {
		t.Fatalf("failStream(refusal) = %v, want the refusal back without touching the stream", err)
	}
}

package providerserver

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"buf.build/go/protovalidate"
	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/naming"
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

func TestEventStreamPropagatesSendError(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("boom")
	var calls int
	sender := newEventStream(context.Background(), func(*progressv1.OperationEvent) error {
		calls++
		if calls == 2 {
			return wantErr
		}
		return nil
	})

	const total = 5
	for range total {
		sender.send(logEvent(testStage.ID, "line"))
	}

	if err := sender.close(); !errors.Is(err, wantErr) {
		t.Fatalf("close() error = %v, want %v", err, wantErr)
	}
	if calls != total {
		t.Fatalf("send attempted for %d events, want %d: a mid-stream error must not skip the rest, including the terminal result", calls, total)
	}
}

func TestEventStreamAppliesBackpressureWithoutDroppingEvents(t *testing.T) {
	t.Parallel()

	stream := &recordingStream{}
	sender := newEventStream(context.Background(), stream.send)

	const total = eventSenderBuffer + 50
	var wg sync.WaitGroup
	for range total {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sender.send(logEvent(testStage.ID, "line"))
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

func TestEventStreamSendRacingCloseNeverPanics(t *testing.T) {
	t.Parallel()

	stream := &recordingStream{}
	sender := newEventStream(context.Background(), stream.send)

	const concurrent = 50
	var wg sync.WaitGroup
	for range concurrent {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sender.send(logEvent(testStage.ID, "racing close"))
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
		sender.send(logEvent(testStage.ID, "after close"))
	}
}

func TestEventStreamSendUnblocksOnContextCancellation(t *testing.T) {
	t.Parallel()

	blockDrain := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())

	sender := newEventStream(ctx, func(*progressv1.OperationEvent) error {
		<-blockDrain
		return nil
	})

	var fillers sync.WaitGroup
	for range eventSenderBuffer + 1 {
		fillers.Add(1)
		go func() {
			defer fillers.Done()
			sender.send(logEvent(testStage.ID, "line"))
		}()
	}
	fillers.Wait()

	cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		sender.send(logEvent(testStage.ID, "blocked"))
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("send did not observe context cancellation once the buffer was saturated")
	}

	close(blockDrain)
	sender.close()
}

func TestEventStreamFailPassesARefusalBackToTheCaller(t *testing.T) {
	t.Parallel()

	stream := &recordingStream{}
	sender := newEventStream(context.Background(), stream.send)

	refusal := connect.NewError(connect.CodeInvalidArgument, errors.New("no"))
	if err := sender.fail(refusal); !errors.Is(err, refusal) {
		t.Fatalf("fail(refusal) = %v, want the refusal returned so the RPC returns the code", err)
	}
	if err := sender.fail(errors.New("the engine gave up")); err != nil {
		t.Fatalf("fail(failure) = %v, want nil so the failure travels as a result event", err)
	}

	if err := sender.close(); err != nil {
		t.Fatalf("close() error = %v", err)
	}
	events := stream.recorded()
	if len(events) != 2 {
		t.Fatalf("got %d events, want a result envelope for the refusal and one for the failure", len(events))
	}
	if result := events[0].GetResult(); !result.GetRefused() || !strings.Contains(result.GetError(), "no") {
		t.Fatalf("result success=%t refused=%t error=%q, want the refusal's envelope marked refused so it is not read as the verdict", result.GetSuccess(), result.GetRefused(), result.GetError())
	}
	if result := events[1].GetResult(); result.GetSuccess() || result.GetRefused() || result.GetError() != "the engine gave up" {
		t.Fatalf("result success=%t refused=%t error=%q, want the failure reported as an unsuccessful result", result.GetSuccess(), result.GetRefused(), result.GetError())
	}
}

func TestAFailedResultIsAnErrorEventAndASuccessfulOneIsInfo(t *testing.T) {
	t.Parallel()

	stream := &recordingStream{}
	sender := newEventStream(context.Background(), stream.send)

	if err := sender.fail(errors.New("the engine gave up")); err != nil {
		t.Fatalf("fail() = %v, want the failure sent as a result", err)
	}
	sender.send(okResult())
	if err := sender.close(); err != nil {
		t.Fatalf("close() error = %v", err)
	}

	events := stream.recorded()
	if got := events[0].GetLevel(); got != progressv1.Level_LEVEL_ERROR {
		t.Errorf("a failed result is %v, want ERROR", got)
	}
	if got := events[1].GetLevel(); got != progressv1.Level_LEVEL_INFO {
		t.Errorf("a successful result is %v, want INFO", got)
	}
}

func TestADegradedNeedIsAWarnEvent(t *testing.T) {
	t.Parallel()

	if got := degradedEvent("web", edge.NeedEdgeMiddleware, "the edge cannot run code").GetLevel(); got != progressv1.Level_LEVEL_WARN {
		t.Errorf("a degraded need is %v, want WARN", got)
	}
}

func TestACheckWarningIsAMessageOnlyEventTheWireAdmits(t *testing.T) {
	t.Parallel()

	if err := protovalidate.Validate(checkWarning("relay", "the plan is unknown")); err != nil {
		t.Errorf("a message-only warning fails the wire's own rules: %v", err)
	}
}

func TestAnUnimplementedFailureIsARefusalOnlyOnAStreamThatSaysSo(t *testing.T) {
	t.Parallel()

	unimplemented := connect.NewError(connect.CodeUnimplemented, errors.New("no connector here"))

	plain := newEventStream(context.Background(), (&recordingStream{}).send)
	if err := plain.fail(unimplemented); err != nil {
		t.Errorf("fail(unimplemented) = %v on a plain stream, want nil: a step that reports it is not built is a failed run, not a malformed request", err)
	}
	if err := plain.close(); err != nil {
		t.Fatalf("close() error = %v", err)
	}

	answering := newEventStream(context.Background(), (&recordingStream{}).send)
	answering.refusing(connect.CodeUnimplemented)
	if err := answering.fail(unimplemented); !errors.Is(err, unimplemented) {
		t.Errorf("fail(unimplemented) = %v on a stream that answers unimplemented, want it returned so the RPC returns the code", err)
	}
	if err := answering.close(); err != nil {
		t.Fatalf("close() error = %v", err)
	}
}

var testStage = PhaseStage(naming.UnitEnvironment, progressv1.Phase_PHASE_PROVISION)

func TestStageProgressTagsEverythingWithItsStage(t *testing.T) {
	t.Parallel()

	stream := &recordingStream{}
	sender := newEventStream(context.Background(), stream.send)
	stage := PhaseStage(naming.UnitEnvironment, progressv1.Phase_PHASE_PROVISION)
	progress := newProgress(sender, stage)

	progress.Say("provisioning the infra stack")
	progress.Detail("engine said something")
	progress.Span("infra", time.Unix(1000, 0), time.Unix(1005, 0), nil)

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
	if events[1].GetLog().GetMessage() != "engine said something" {
		t.Errorf("Detail() log = %q", events[1].GetLog().GetMessage())
	}
	if StageID(events[1].GetLog().GetStageId()) != stage.ID {
		t.Errorf("Detail() StageId = %x, want %x", events[1].GetLog().GetStageId(), stage.ID)
	}
	if span := events[2].GetSpan(); StageID(span.GetParentSpanId()) != stage.ID {
		t.Errorf("Span() ParentSpanId = %x, want the reporter's stage %x", span.GetParentSpanId(), stage.ID)
	}
}

func TestEveryEventAStageSendsCarriesATimeALevelAndTheStagesPhase(t *testing.T) {
	t.Parallel()

	stream := &recordingStream{}
	sender := newEventStream(context.Background(), stream.send)
	stage := PhaseStage(naming.UnitEnvironment, progressv1.Phase_PHASE_PROVISION)
	progress := newProgress(sender, stage)

	before := time.Now().UnixNano()
	progress.Say("provisioning the infra stack")
	progress.Detail("engine said something")
	progress.Span("infra", time.Unix(1000, 0), time.Unix(1005, 0), nil)
	after := time.Now().UnixNano()

	if err := sender.close(); err != nil {
		t.Fatalf("close() error = %v", err)
	}
	events := stream.recorded()
	if len(events) != 3 {
		t.Fatalf("got %d events, want 3", len(events))
	}
	for i, event := range events {
		if at := event.GetTimeUnixNano(); at < before || at > after {
			t.Errorf("event %d is stamped %d, want the time it was sent, between %d and %d", i, at, before, after)
		}
		if event.GetLevel() != progressv1.Level_LEVEL_INFO {
			t.Errorf("event %d level = %v, want INFO", i, event.GetLevel())
		}
		if event.GetPhase() != progressv1.Phase_PHASE_PROVISION {
			t.Errorf("event %d phase = %v, want the stage's provision phase", i, event.GetPhase())
		}
	}
	for i, event := range events[:2] {
		if string(event.GetSpanId()) != string(stage.ID[:]) {
			t.Errorf("event %d span id = %x, want the stage's id %x", i, event.GetSpanId(), stage.ID)
		}
	}
	if span := events[2]; len(span.GetSpanId()) == 0 || string(span.GetSpanId()) != string(span.GetSpan().GetSpanId()) {
		t.Errorf("a span's envelope names span %x, want the span it closes, %x", span.GetSpanId(), span.GetSpan().GetSpanId())
	}
}

func TestAStagesWarningIsAMessageOnlyWarnScopedToTheStage(t *testing.T) {
	t.Parallel()

	stream := &recordingStream{}
	sender := newEventStream(context.Background(), stream.send)
	progress := newProgress(sender, testStage)

	progress.Warn("the old binding outlived its release\x1b[2J")

	if err := sender.close(); err != nil {
		t.Fatalf("close() error = %v", err)
	}
	events := stream.recorded()
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	warned := events[0]
	if warned.GetLevel() != progressv1.Level_LEVEL_WARN {
		t.Errorf("level = %v, want WARN", warned.GetLevel())
	}
	if warned.GetMessage() != "the old binding outlived its release[2J" {
		t.Errorf("message = %q, want the warning with its control characters gone", warned.GetMessage())
	}
	if warned.GetEvent() != nil {
		t.Errorf("body = %v, want a message-only event", warned.GetEvent())
	}
	if warned.GetPhase() != testStage.Phase || StageID(warned.GetSpanId()) != testStage.ID {
		t.Errorf("scope = %v %x, want the stage's %v %x", warned.GetPhase(), warned.GetSpanId(), testStage.Phase, testStage.ID)
	}
	if err := protovalidate.Validate(warned); err != nil {
		t.Errorf("the warning fails the wire's own rules: %v", err)
	}
}

func TestAStagesDebugLineIsADebugLogScopedToTheStage(t *testing.T) {
	t.Parallel()

	stream := &recordingStream{}
	sender := newEventStream(context.Background(), stream.send)
	progress := newProgress(sender, testStage)

	progress.Debug("+  aws:s3:Bucket assets creating (0s)")

	if err := sender.close(); err != nil {
		t.Fatalf("close() error = %v", err)
	}
	events := stream.recorded()
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	line := events[0]
	if line.GetLevel() != progressv1.Level_LEVEL_DEBUG {
		t.Errorf("level = %v, want DEBUG", line.GetLevel())
	}
	if got := line.GetLog(); got.GetMessage() != "+  aws:s3:Bucket assets creating (0s)" || StageID(got.GetStageId()) != testStage.ID {
		t.Errorf("log = %q in %x, want the line in the stage %x", got.GetMessage(), got.GetStageId(), testStage.ID)
	}
	if line.GetMessage() != line.GetLog().GetMessage() {
		t.Errorf("envelope message = %q, want the line", line.GetMessage())
	}
	if line.GetPhase() != testStage.Phase || StageID(line.GetSpanId()) != testStage.ID {
		t.Errorf("scope = %v %x, want the stage's %v %x", line.GetPhase(), line.GetSpanId(), testStage.Phase, testStage.ID)
	}
}

func TestProgressMessagesTravelOnTheEnvelope(t *testing.T) {
	t.Parallel()

	stream := &recordingStream{}
	sender := newEventStream(context.Background(), stream.send)
	progress := newProgress(sender, testStage)

	progress.Say("provisioning the infra stack")
	progress.Detail("engine said something")

	if err := sender.close(); err != nil {
		t.Fatalf("close() error = %v", err)
	}
	for i, want := range []string{"provisioning the infra stack", "engine said something"} {
		if got := stream.recorded()[i].GetMessage(); got != want {
			t.Errorf("event %d message = %q, want %q", i, got, want)
		}
	}
}

func TestStageProgressStripsControlCharacters(t *testing.T) {
	t.Parallel()

	stream := &recordingStream{}
	sender := newEventStream(context.Background(), stream.send)
	progress := newProgress(sender, testStage)

	progress.Say("clearing the screen\x1b[2J now")

	if err := sender.close(); err != nil {
		t.Fatalf("close() error = %v", err)
	}
	if got := stream.recorded()[0].GetProgress().GetMessage(); got != "clearing the screen[2J now" {
		t.Errorf("Say() message = %q, want the control characters gone", got)
	}
}

func TestEventConstructors(t *testing.T) {
	t.Parallel()

	stage := testStage

	if got := stageProgressEvent(stage.ID, "deleting").GetProgress(); StageID(got.GetStageId()) != stage.ID {
		t.Errorf("stageProgressEvent() StageId = %x, want %x", got.GetStageId(), stage.ID)
	}
	if got := stageProgressEvent(stage.ID, "deleting").GetProgress().GetMessage(); got != "deleting" {
		t.Errorf("stageProgressEvent() message = %q", got)
	}

	degraded := degradedEvent("web", edge.NeedEdgeMiddleware, "the edge cannot run code").GetDegraded()
	if degraded.GetNeed() != string(edge.NeedEdgeMiddleware) {
		t.Errorf("degradedEvent() Need = %q", degraded.GetNeed())
	}

	manual := dnsManualRecordsEvent("add these", []edge.Record{{Name: "app.example.com", Type: edge.RecordTypeCNAME, Value: "front"}}, "note").GetDnsManualRecords()
	if len(manual.GetRecords()) != 1 || manual.GetRecords()[0].GetName() != "app.example.com" {
		t.Errorf("dnsManualRecordsEvent() records = %+v", manual.GetRecords())
	}

	if !okResult().GetResult().GetSuccess() {
		t.Error("okResult() is not a success")
	}
}

func TestRefusedRequestNamesAnInvalidArgument(t *testing.T) {
	t.Parallel()

	if !refusedRequest(connect.NewError(connect.CodeInvalidArgument, errors.New("no"))) {
		t.Error("refusedRequest() = false for an InvalidArgument error")
	}
	if refusedRequest(errors.New("no")) {
		t.Error("refusedRequest() = true for a plain error, which ends the run rather than refusing the request")
	}
}

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

	"github.com/ocelhq/ocel/pkg/edge"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	"github.com/ocelhq/ocel/pkg/provider"
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
		sender.send(outputEvent(progressv1.Level_LEVEL_INFO, "line"))
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
			sender.send(outputEvent(progressv1.Level_LEVEL_INFO, "line"))
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
			sender.send(outputEvent(progressv1.Level_LEVEL_INFO, "racing close"))
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
		sender.send(outputEvent(progressv1.Level_LEVEL_INFO, "after close"))
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
			sender.send(outputEvent(progressv1.Level_LEVEL_INFO, "line"))
		}()
	}
	fillers.Wait()

	cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		sender.send(outputEvent(progressv1.Level_LEVEL_INFO, "blocked"))
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("send did not observe context cancellation once the buffer was saturated")
	}

	close(blockDrain)
	sender.close()
}

func TestEventStreamFailPassesAQuestionBackToTheCallerAndReportsNoResult(t *testing.T) {
	t.Parallel()

	stream := &recordingStream{}
	sender := newEventStream(context.Background(), stream.send)

	asked := provider.RefusalError(provider.Ask("record the key and try again", provider.Question{Prompt: "Trust it?"}))
	if err := sender.fail(asked); !errors.Is(err, asked) {
		t.Fatalf("fail(question) = %v, want the question returned so the caller can answer it", err)
	}
	if err := sender.close(); err != nil {
		t.Fatalf("close() error = %v", err)
	}
	if events := stream.recorded(); len(events) != 0 {
		t.Errorf("got %d events, want no result: a question is not the run's verdict", len(events))
	}
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

func TestADegradedNeedIsAWarnNamingTheAppTheNeedAndTheDegrade(t *testing.T) {
	t.Parallel()

	event := degradedEvent("web", edge.NeedEdgeMiddleware, "the edge cannot run code")
	if event.GetLevel() != progressv1.Level_LEVEL_WARN || event.GetPhase() != progressv1.Phase_PHASE_CHECK {
		t.Errorf("a degraded need is %v in %v, want WARN in the check phase", event.GetLevel(), event.GetPhase())
	}
	if event.GetSubject() != "web" {
		t.Errorf("subject = %q, want the app", event.GetSubject())
	}
	if want := "edge-middleware runs degraded: the edge cannot run code"; event.GetMessage() != want {
		t.Errorf("message = %q, want %q", event.GetMessage(), want)
	}
	if event.GetBody() != nil {
		t.Errorf("body = %T, want a message-only event", event.GetBody())
	}
	if err := protovalidate.Validate(event); err != nil {
		t.Errorf("a degraded need fails the wire's own rules: %v", err)
	}
}

func TestACheckWarningIsAMessageOnlyEventTheWireAdmits(t *testing.T) {
	t.Parallel()

	if err := protovalidate.Validate(checkWarning("relay", "the plan is unknown")); err != nil {
		t.Errorf("a message-only warning fails the wire's own rules: %v", err)
	}
}

func TestACheckWarningAndADegradedNeedCarryNoControlCharactersFromARemoteReason(t *testing.T) {
	t.Parallel()

	remote := "the plan is unknown: api answered \x1b[2J\x1b]8;;https://evil.example\x07click\r\n"
	for name, event := range map[string]*progressv1.OperationEvent{
		"check warning": checkWarning("relay", remote),
		"degraded need": degradedEvent("web", edge.NeedEdgeMiddleware, remote),
	} {
		if strings.ContainsAny(event.GetMessage(), "\x1b\x07\r\n") {
			t.Errorf("the %s's message is %q, want the control characters a remote api sent stripped", name, event.GetMessage())
		}
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

var testSpan = environmentUnit(progressv1.Phase_PHASE_PROVISION)

func TestASpanSaysAMessageOnlyLineWritesOutputAndOpensAndEndsItsDetailSpans(t *testing.T) {
	t.Parallel()

	stream := &recordingStream{}
	sender := newEventStream(context.Background(), stream.send)
	span := environmentUnit(progressv1.Phase_PHASE_PROVISION)
	progress := newSpanLog(sender, span)

	progress.Say("provisioning the infra stack")
	progress.Detail("engine said something")
	progress.Span("dns records", time.Unix(1000, 0), time.Unix(1005, 0), nil)

	if err := sender.close(); err != nil {
		t.Fatalf("close() error = %v", err)
	}
	events := stream.recorded()
	if len(events) != 4 {
		t.Fatalf("got %d events, want a message, an output line, and a detail span's start and end", len(events))
	}

	said, wrote, opened, closed := events[0], events[1], events[2], events[3]
	if said.GetBody() != nil || said.GetMessage() != "provisioning the infra stack" || SpanID(said.GetSpanId()) != span.ID {
		t.Errorf("Say() = %T %q in %x, want a message-only line in the span %x", said.GetBody(), said.GetMessage(), said.GetSpanId(), span.ID)
	}
	if wrote.GetOutput() == nil || wrote.GetMessage() != "engine said something" || SpanID(wrote.GetSpanId()) != span.ID {
		t.Errorf("Detail() = %T %q in %x, want an output line in the span %x", wrote.GetBody(), wrote.GetMessage(), wrote.GetSpanId(), span.ID)
	}
	if opened.GetStarted() == nil || opened.GetMessage() != "dns records" || SpanID(opened.GetStarted().GetParentSpanId()) != span.ID {
		t.Errorf("Span() opens %T %q under %x, want the detail span \"dns records\" under the span %x", opened.GetBody(), opened.GetMessage(), opened.GetStarted().GetParentSpanId(), span.ID)
	}
	if closed.GetEnded() == nil || string(closed.GetSpanId()) != string(opened.GetSpanId()) {
		t.Errorf("Span() closes %T %x, want the span it opened, %x", closed.GetBody(), closed.GetSpanId(), opened.GetSpanId())
	}
	if opened.GetTime().AsTime().UnixNano() != time.Unix(1000, 0).UnixNano() || closed.GetTime().AsTime().UnixNano() != time.Unix(1005, 0).UnixNano() {
		t.Errorf("the detail span runs %d to %d, want its own start and end", opened.GetTime().AsTime().UnixNano(), closed.GetTime().AsTime().UnixNano())
	}
	for i, event := range events {
		if err := protovalidate.Validate(event); err != nil {
			t.Errorf("event %d fails the wire's own rules: %v", i, err)
		}
	}
}

func TestEveryEventASpanSendsCarriesATimeALevelAndTheSpansPhase(t *testing.T) {
	t.Parallel()

	stream := &recordingStream{}
	sender := newEventStream(context.Background(), stream.send)
	span := environmentUnit(progressv1.Phase_PHASE_PROVISION)
	progress := newSpanLog(sender, span)

	before := time.Now().UnixNano()
	progress.Say("provisioning the infra stack")
	progress.Detail("engine said something")
	after := time.Now().UnixNano()
	progress.Span("dns records", time.Unix(1000, 0), time.Unix(1005, 0), nil)

	if err := sender.close(); err != nil {
		t.Fatalf("close() error = %v", err)
	}
	events := stream.recorded()
	if len(events) != 4 {
		t.Fatalf("got %d events, want 4", len(events))
	}
	for i, event := range events[:2] {
		if at := event.GetTime().AsTime().UnixNano(); at < before || at > after {
			t.Errorf("event %d is stamped %d, want the time it was sent, between %d and %d", i, at, before, after)
		}
		if string(event.GetSpanId()) != string(span.ID[:]) {
			t.Errorf("event %d span id = %x, want the span's id %x", i, event.GetSpanId(), span.ID)
		}
	}
	for i, event := range events {
		if event.GetLevel() != progressv1.Level_LEVEL_INFO {
			t.Errorf("event %d level = %v, want INFO", i, event.GetLevel())
		}
		if event.GetPhase() != progressv1.Phase_PHASE_PROVISION {
			t.Errorf("event %d phase = %v, want the span's provision phase", i, event.GetPhase())
		}
	}
}

func TestASpansWarningIsAMessageOnlyWarnInTheSpan(t *testing.T) {
	t.Parallel()

	stream := &recordingStream{}
	sender := newEventStream(context.Background(), stream.send)
	progress := newSpanLog(sender, testSpan)

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
	if warned.GetBody() != nil {
		t.Errorf("body = %v, want a message-only event", warned.GetBody())
	}
	if warned.GetPhase() != testSpan.Phase || SpanID(warned.GetSpanId()) != testSpan.ID {
		t.Errorf("span = %v %x, want the span's %v %x", warned.GetPhase(), warned.GetSpanId(), testSpan.Phase, testSpan.ID)
	}
	if err := protovalidate.Validate(warned); err != nil {
		t.Errorf("the warning fails the wire's own rules: %v", err)
	}
}

func TestASpansErrorIsAMessageOnlyErrorInTheSpan(t *testing.T) {
	t.Parallel()

	stream := &recordingStream{}
	sender := newEventStream(context.Background(), stream.send)
	progress := newSpanLog(sender, testSpan)

	progress.Error("logs (aws:s3/bucket:Bucket): BucketAlreadyExists\x1b[2J")

	if err := sender.close(); err != nil {
		t.Fatalf("close() error = %v", err)
	}
	events := stream.recorded()
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	failed := events[0]
	if failed.GetLevel() != progressv1.Level_LEVEL_ERROR {
		t.Errorf("level = %v, want ERROR", failed.GetLevel())
	}
	if failed.GetMessage() != "logs (aws:s3/bucket:Bucket): BucketAlreadyExists[2J" {
		t.Errorf("message = %q, want the error with its control characters gone", failed.GetMessage())
	}
	if failed.GetBody() != nil {
		t.Errorf("body = %v, want a message-only event", failed.GetBody())
	}
	if failed.GetPhase() != testSpan.Phase || SpanID(failed.GetSpanId()) != testSpan.ID {
		t.Errorf("span = %v %x, want the span's %v %x", failed.GetPhase(), failed.GetSpanId(), testSpan.Phase, testSpan.ID)
	}
	if err := protovalidate.Validate(failed); err != nil {
		t.Errorf("the error fails the wire's own rules: %v", err)
	}
}

func TestASpansDebugLineIsADebugOutputLineInTheSpan(t *testing.T) {
	t.Parallel()

	stream := &recordingStream{}
	sender := newEventStream(context.Background(), stream.send)
	progress := newSpanLog(sender, testSpan)

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
	if line.GetOutput() == nil || line.GetMessage() != "+  aws:s3:Bucket assets creating (0s)" {
		t.Errorf("Debug() = %T %q, want the line as output", line.GetBody(), line.GetMessage())
	}
	if line.GetPhase() != testSpan.Phase || SpanID(line.GetSpanId()) != testSpan.ID {
		t.Errorf("span = %v %x, want the span's %v %x", line.GetPhase(), line.GetSpanId(), testSpan.Phase, testSpan.ID)
	}
}

func TestProgressMessagesTravelOnTheEnvelope(t *testing.T) {
	t.Parallel()

	stream := &recordingStream{}
	sender := newEventStream(context.Background(), stream.send)
	progress := newSpanLog(sender, testSpan)

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

func TestSpanProgressStripsControlCharacters(t *testing.T) {
	t.Parallel()

	stream := &recordingStream{}
	sender := newEventStream(context.Background(), stream.send)
	progress := newSpanLog(sender, testSpan)

	progress.Say("clearing the screen\x1b[2J now")

	if err := sender.close(); err != nil {
		t.Fatalf("close() error = %v", err)
	}
	if got := stream.recorded()[0].GetMessage(); got != "clearing the screen[2J now" {
		t.Errorf("Say() message = %q, want the control characters gone", got)
	}
}

func TestEventConstructors(t *testing.T) {
	t.Parallel()

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

package providerserver

import (
	"context"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"buf.build/go/protovalidate"

	"github.com/ocelhq/ocel/pkg/naming"

	"github.com/ocelhq/ocel/pkg/progress"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

var environmentTitle = progress.Checking.Title("the bootstrap, domains and bindings for shop")

func environmentSpan(phase progressv1.Phase) Span {
	return RootSpan(naming.SpanEnvironment, "production", environmentTitle, phase)
}

func TestAStartedSpanIsTitledUnderItsParent(t *testing.T) {
	t.Parallel()

	stream := &recordingStream{}
	sender := newEventStream(context.Background(), stream.send)
	tracer := newSpanEvents(sender)

	root := environmentSpan(progressv1.Phase_PHASE_PROVISION)
	detail := NewSpan(root, "dns records")
	tracer.Start(time.Now(), root, detail)

	if err := sender.close(); err != nil {
		t.Fatalf("close() error = %v", err)
	}
	events := stream.recorded()
	if len(events) != 2 {
		t.Fatalf("got %d events, want one started event per span", len(events))
	}

	opened, nested := events[0], events[1]
	if opened.GetStarted() == nil || nested.GetStarted() == nil {
		t.Fatalf("bodies = %T, %T, want both started", opened.GetBody(), nested.GetBody())
	}
	if opened.GetMessage() != environmentTitle.Started || opened.GetSubject() != "production" || SpanID(opened.GetSpanId()) != root.ID {
		t.Errorf("the span starts as %q: %q %x, want \"production\": %q %x", opened.GetSubject(), opened.GetMessage(), opened.GetSpanId(), environmentTitle.Started, root.ID)
	}
	if len(opened.GetStarted().GetParentSpanId()) != 0 {
		t.Errorf("span parent = %x, want none (a span is a root)", opened.GetStarted().GetParentSpanId())
	}
	if got := opened.GetPhase(); got != progressv1.Phase_PHASE_PROVISION {
		t.Errorf("span phase = %v, want the provision phase it runs in", got)
	}
	if SpanID(nested.GetStarted().GetParentSpanId()) != root.ID {
		t.Errorf("detail parent = %x, want the span %x", nested.GetStarted().GetParentSpanId(), root.ID)
	}
	if nested.GetMessage() != "dns records" || nested.GetSubject() != "production" || nested.GetPhase() != progressv1.Phase_PHASE_PROVISION {
		t.Errorf("the detail starts as %q: %q in %v, want its span's \"production\": \"dns records\" in the provision phase",
			nested.GetSubject(), nested.GetMessage(), nested.GetPhase())
	}
	for i, event := range events {
		if err := protovalidate.Validate(event); err != nil {
			t.Errorf("started event %d fails the wire's own rules: %v", i, err)
		}
	}
}

func TestSpanIDsAreTheSharedNamingDigests(t *testing.T) {
	t.Parallel()

	stream := &recordingStream{}
	sender := newEventStream(context.Background(), stream.send)
	tracer := newSpanEvents(sender)

	tracer.Start(time.Now(),
		environmentSpan(progressv1.Phase_PHASE_PROVISION),
		RootSpan(naming.SpanEdge, "cloudflare", progress.Reconciling.Title("the routes for shop in production"), progressv1.Phase_PHASE_DEPLOY),
		RootSpan(naming.SpanPromotion, "production", progress.Switching.Title("traffic to promotion p1"), progressv1.Phase_PHASE_PROMOTE),
	)

	if err := sender.close(); err != nil {
		t.Fatalf("close() error = %v", err)
	}
	events := stream.recorded()
	for i, want := range []string{
		"9f2ecbbdfa2db89d",
		"000c0a32c587a5a8",
		"17505ced11f71bd3",
	} {
		if got := hex.EncodeToString(events[i].GetSpanId()); got != want {
			t.Errorf("span %d id = %s, want the naming digest %s", i, got, want)
		}
		if len(events[i].GetSpanId()) != naming.SpanIDLen {
			t.Errorf("span %d id is %d bytes, want %d", i, len(events[i].GetSpanId()), naming.SpanIDLen)
		}
	}
}

func TestDetailSpansMintTheirOwnIDUnderTheirSpan(t *testing.T) {
	t.Parallel()

	root := RootSpan(naming.SpanPromotion, "production", progress.Switching.Title("traffic to promotion p1"), progressv1.Phase_PHASE_PROMOTE)
	first := NewSpan(root, "detail")
	second := NewSpan(root, "detail")

	if first.ID == second.ID {
		t.Error("two detail spans share an id, want each minted on its own")
	}
	if first.ParentID != root.ID {
		t.Error("a detail span hangs off something other than its span")
	}
}

func TestAnEndedSpanNamesItsSpanEndsAtItsEndAndCarriesItsStartAndAttributes(t *testing.T) {
	t.Parallel()

	stream := &recordingStream{}
	sender := newEventStream(context.Background(), stream.send)
	tracer := newSpanEvents(sender)

	root := environmentSpan(progressv1.Phase_PHASE_PROVISION)
	child := NewSpan(root, "web")
	start := time.Unix(1000, 0)
	end := time.Unix(1005, 0)
	tracer.End(child, start, end, nil, provider.AttrApp("web"), provider.AttrResourceCount(3))

	if err := sender.close(); err != nil {
		t.Fatalf("close() error = %v", err)
	}
	event := stream.recorded()[0]
	ended := event.GetEnded()
	if ended == nil {
		t.Fatalf("body = %T, want ended", event.GetBody())
	}
	if SpanID(event.GetSpanId()) != child.ID {
		t.Errorf("span id = %x, want the span's id %x", event.GetSpanId(), child.ID)
	}
	if event.GetPhase() != progressv1.Phase_PHASE_PROVISION {
		t.Errorf("phase = %v, want the provision phase", event.GetPhase())
	}
	if ended.GetStatus() != progressv1.SpanStatus_SPAN_STATUS_OK {
		t.Errorf("status = %v, want OK", ended.GetStatus())
	}
	if ended.GetStartTimeUnixNano() != start.UnixNano() || event.GetTime().AsTime().UnixNano() != end.UnixNano() {
		t.Errorf("times = %d/%d, want the span's start %d and its end %d on the envelope", ended.GetStartTimeUnixNano(), event.GetTime().AsTime().UnixNano(), start.UnixNano(), end.UnixNano())
	}
	if got := attributeValue(ended.GetAttributes(), progressv1.AttributeKey_ATTRIBUTE_KEY_APP); got != "web" {
		t.Errorf("APP attribute = %q, want the string key a provider sets mapped onto the wire enum", got)
	}
	if got := attributeValue(ended.GetAttributes(), progressv1.AttributeKey_ATTRIBUTE_KEY_RESOURCE_COUNT); got != "3" {
		t.Errorf("RESOURCE_COUNT attribute = %q", got)
	}
	if err := protovalidate.Validate(event); err != nil {
		t.Errorf("the ended event fails the wire's own rules: %v", err)
	}
}

func TestAResourcesActionReachesTheWireAsItsOwnAttribute(t *testing.T) {
	t.Parallel()

	stream := &recordingStream{}
	sender := newEventStream(context.Background(), stream.send)
	tracer := newSpanEvents(sender)

	child := NewSpan(environmentSpan(progressv1.Phase_PHASE_PROVISION), "create resource")
	tracer.End(child, time.Unix(1000, 0), time.Unix(1001, 0), nil,
		provider.AttrResourceType("aws:s3/bucket:Bucket"), provider.AttrResourceName("assets"), provider.AttrResourceAction(provider.ActionCreate))

	if err := sender.close(); err != nil {
		t.Fatalf("close() error = %v", err)
	}
	attrs := stream.recorded()[0].GetEnded().GetAttributes()
	if got := attributeValue(attrs, progressv1.AttributeKey_ATTRIBUTE_KEY_RESOURCE_ACTION); got != string(provider.ActionCreate) {
		t.Errorf("RESOURCE_ACTION attribute = %q, want %q", got, provider.ActionCreate)
	}
}

func TestAFailedSpanEndsWithAnErrorKindNeverRawText(t *testing.T) {
	t.Parallel()

	stream := &recordingStream{}
	sender := newEventStream(context.Background(), stream.send)
	tracer := newSpanEvents(sender)

	secret := "postgres://user:hunter2@10.0.0.1:5432/db AKIAABCDEF1234567890"
	span := environmentSpan(progressv1.Phase_PHASE_PROVISION)
	tracer.End(span, time.Now(), time.Now(), errors.New(secret))

	if err := sender.close(); err != nil {
		t.Fatalf("close() error = %v", err)
	}
	event := stream.recorded()[0]
	ended := event.GetEnded()
	if ended.GetStatus() != progressv1.SpanStatus_SPAN_STATUS_ERROR {
		t.Fatalf("status = %v, want ERROR", ended.GetStatus())
	}
	got := attributeValue(ended.GetAttributes(), progressv1.AttributeKey_ATTRIBUTE_KEY_ERROR_KIND)
	if got == "" {
		t.Fatal("no ATTRIBUTE_KEY_ERROR_KIND attribute on a failed span")
	}
	if strings.Contains(got, "hunter2") || strings.Contains(event.GetMessage(), "hunter2") {
		t.Fatal("the ended event carries the raw error text")
	}
	if got != provider.ErrorKindFailed {
		t.Errorf("ERROR_KIND = %q, want a bounded classification", got)
	}
}

func TestAFailedSpanEndsAtErrorLevelAndASucceededOneAtInfo(t *testing.T) {
	t.Parallel()

	stream := &recordingStream{}
	sender := newEventStream(context.Background(), stream.send)
	tracer := newSpanEvents(sender)

	span := environmentSpan(progressv1.Phase_PHASE_PROVISION)
	tracer.End(span, time.Now(), time.Now(), errors.New("the stack refused"))
	tracer.End(span, time.Now(), time.Now(), nil)

	if err := sender.close(); err != nil {
		t.Fatalf("close() error = %v", err)
	}
	events := stream.recorded()
	if got := events[0].GetLevel(); got != progressv1.Level_LEVEL_ERROR {
		t.Errorf("a failed span ends at %v, want LEVEL_ERROR", got)
	}
	if got := events[1].GetLevel(); got != progressv1.Level_LEVEL_INFO {
		t.Errorf("a succeeded span ends at %v, want LEVEL_INFO", got)
	}
}

func TestASucceededSpanEndsTitledWithWhatItDidAndAFailedOneWithNoTitle(t *testing.T) {
	t.Parallel()

	stream := &recordingStream{}
	sender := newEventStream(context.Background(), stream.send)
	tracer := newSpanEvents(sender)

	span := environmentSpan(progressv1.Phase_PHASE_PROVISION)
	tracer.End(span, time.Now(), time.Now(), errors.New("the stack refused"))
	tracer.End(span, time.Now(), time.Now(), nil)

	if err := sender.close(); err != nil {
		t.Fatalf("close() error = %v", err)
	}
	events := stream.recorded()
	if got := events[0].GetEnded().GetTitle(); got != "" {
		t.Errorf("a failed span ends titled %q, want no title", got)
	}
	if got, want := events[1].GetEnded().GetTitle(), "Checked the bootstrap, domains and bindings for shop"; got != want {
		t.Errorf("a succeeded span ends titled %q, want %q", got, want)
	}
}

func TestSpanTitlesAreSanitized(t *testing.T) {
	t.Parallel()

	if got := RootSpan(naming.SpanEnvironment, "production", progress.Title{Started: "\x1b[2J", Ended: "\x1b[2J"}, progressv1.Phase_PHASE_PROVISION).Title; got.Started != "[2J" || got.Ended != "[2J" {
		t.Errorf("RootSpan() title = %q, want the control characters gone", got)
	}
	if got := RootSpan(naming.SpanEnvironment, "production", progress.Title{Started: "   ", Ended: "   "}, progressv1.Phase_PHASE_PROVISION).Title; got.Started != "span" || got.Ended != "span" {
		t.Errorf("RootSpan() title = %q, want a fallback title", got)
	}
	long := strings.Repeat("a", progress.MaxSpanNameLen*2)
	if got := RootSpan(naming.SpanEnvironment, "production", progress.Title{Started: long, Ended: long}, progressv1.Phase_PHASE_PROVISION).Title; len(got.Started) > progress.MaxSpanNameLen || len(got.Ended) > progress.MaxSpanNameLen {
		t.Errorf("RootSpan() title is %d and %d long, want each capped at %d", len(got.Started), len(got.Ended), progress.MaxSpanNameLen)
	}
}

func attributeValue(attrs []*progressv1.SpanAttribute, key progressv1.AttributeKey) string {
	for _, a := range attrs {
		if a.GetKey() == key {
			return a.GetValue()
		}
	}
	return ""
}

func reasonsSaid(events []*progressv1.OperationEvent) []*progressv1.OperationEvent {
	var said []*progressv1.OperationEvent
	for _, event := range events {
		if event.GetBody() == nil && event.GetLevel() == progressv1.Level_LEVEL_ERROR {
			said = append(said, event)
		}
	}
	return said
}

func TestAFailedSpanSaysWhyAtErrorInItsSpanOnceBeforeTheSpanEnds(t *testing.T) {
	t.Parallel()

	stream := &recordingStream{}
	sender := newEventStream(context.Background(), stream.send)
	root := RootSpan("web", "web", progress.Deploying.Title("the serverless app to production"), progressv1.Phase_PHASE_DEPLOY)
	_ = newSpanEvents(sender).run(root, func(u *spanRun) error {
		return u.phase(func(progress.Log) error {
			return errors.New("the web stack could not be provisioned\x1b[0m")
		})
	})

	if err := sender.close(); err != nil {
		t.Fatalf("close() error = %v", err)
	}
	events := stream.recorded()
	said := reasonsSaid(events)
	if len(said) != 1 {
		t.Fatalf("the failure is said %d times, want once", len(said))
	}
	reason := said[0]
	if SpanID(reason.GetSpanId()) != root.ID || reason.GetPhase() != progressv1.Phase_PHASE_DEPLOY {
		t.Errorf("the reason is scoped to %x in %v, want the span %x in the deploy phase", reason.GetSpanId(), reason.GetPhase(), root.ID)
	}
	if got, want := reason.GetMessage(), "the web stack could not be provisioned[0m"; got != want {
		t.Errorf("the reason reads %q, want the error sanitized like any message: %q", got, want)
	}
	at := slices.Index(events, reason)
	for _, event := range events[:at] {
		if event.GetEnded() != nil {
			t.Fatalf("a span ended before the reason was said, want the reason inside its span")
		}
	}
	for _, event := range events {
		if event.GetEnded() != nil && event.GetMessage() != "" {
			t.Errorf("an Ended carries %q, want Ended without text", event.GetMessage())
		}
	}
}

func TestASpanWhoseWorkAsksAQuestionNeitherFailsNorSaysItAtError(t *testing.T) {
	t.Parallel()

	stream := &recordingStream{}
	sender := newEventStream(context.Background(), stream.send)
	root := RootSpan("web", "web", progress.Updating.Title("bootstrap stack box"), progressv1.Phase_PHASE_PROVISION)
	err := newSpanEvents(sender).run(root, func(u *spanRun) error {
		return u.phase(func(progress.Log) error {
			return provider.Ask("ports 80 and 443 are closed", provider.Question{Finding: "ports 80 and 443 are closed", Prompt: "Have you opened them?"})
		})
	})
	if _, asked := provider.QuestionOf(err); !asked {
		t.Fatalf("run() = %v, want the question handed back to ask", err)
	}

	if err := sender.close(); err != nil {
		t.Fatalf("close() error = %v", err)
	}
	events := stream.recorded()
	if said := reasonsSaid(events); len(said) != 0 {
		t.Errorf("said %q at error, want nothing: the finding is shown with the question, and nothing failed yet", said[0].GetMessage())
	}
	ended := events[len(events)-1].GetEnded()
	if ended == nil || ended.GetStatus() == progressv1.SpanStatus_SPAN_STATUS_ERROR {
		t.Fatalf("the span ended %v, want it ended without failing while it waits on an answer", ended)
	}
	if !strings.Contains(ended.GetTitle(), "waiting on your answer") {
		t.Errorf("the span ended titled %q, want it to say it waits on your answer", ended.GetTitle())
	}
}

func TestASpansWorkSpeaksInTheSpansOwnSpanWithNoSpanOfItsOwn(t *testing.T) {
	t.Parallel()

	stream := &recordingStream{}
	sender := newEventStream(context.Background(), stream.send)
	root := RootSpan("web", "web", progress.Deploying.Title("the serverless app to production"), progressv1.Phase_PHASE_DEPLOY)
	_ = newSpanEvents(sender).run(root, func(u *spanRun) error {
		return u.phase(func(progress progress.Log) error {
			progress.Say("Uploading function web's artifact (1.2 MiB)")
			return nil
		})
	})

	if err := sender.close(); err != nil {
		t.Fatalf("close() error = %v", err)
	}
	var started int
	for _, event := range stream.recorded() {
		if SpanID(event.GetSpanId()) != root.ID {
			t.Errorf("an event is scoped to %x, want every one in the span's span %x", event.GetSpanId(), root.ID)
		}
		if event.GetStarted() != nil {
			started++
		}
	}
	if started != 1 {
		t.Errorf("%d spans started, want only the span's", started)
	}
}

func TestASpanThatFailsOutsideItsPhaseSaysWhyInItsOwnSpan(t *testing.T) {
	t.Parallel()

	stream := &recordingStream{}
	sender := newEventStream(context.Background(), stream.send)
	root := RootSpan("web", "web", progress.Deploying.Title("the serverless app to production"), progressv1.Phase_PHASE_DEPLOY)
	_ = newSpanEvents(sender).run(root, func(*spanRun) error {
		return errors.New("the web stack is locked")
	})

	if err := sender.close(); err != nil {
		t.Fatalf("close() error = %v", err)
	}
	said := reasonsSaid(stream.recorded())
	if len(said) != 1 || SpanID(said[0].GetSpanId()) != root.ID || said[0].GetMessage() != "the web stack is locked" {
		t.Fatalf("said %d reasons, want the reason once in the span's own span", len(said))
	}
}

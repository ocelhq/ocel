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

const environmentTitle = "Checking the bootstrap, domains and bindings for shop"

func environmentUnit(phase progressv1.Phase) Stage {
	return UnitStage(naming.UnitEnvironment, "production", environmentTitle, phase)
}

func TestAStartedStageIsTitledUnderItsParent(t *testing.T) {
	t.Parallel()

	stream := &recordingStream{}
	sender := newEventStream(context.Background(), stream.send)
	tracer := newEventTrace(sender)

	unit := environmentUnit(progressv1.Phase_PHASE_PROVISION)
	detail := NewStage(unit, "dns records")
	tracer.Start(time.Now(), unit, detail)

	if err := sender.close(); err != nil {
		t.Fatalf("close() error = %v", err)
	}
	events := stream.recorded()
	if len(events) != 2 {
		t.Fatalf("got %d events, want one started event per stage", len(events))
	}

	opened, nested := events[0], events[1]
	if opened.GetStarted() == nil || nested.GetStarted() == nil {
		t.Fatalf("bodies = %T, %T, want both started", opened.GetBody(), nested.GetBody())
	}
	if opened.GetMessage() != environmentTitle || opened.GetSubject() != "production" || StageID(opened.GetSpanId()) != unit.ID {
		t.Errorf("the unit starts as %q: %q %x, want \"production\": %q %x", opened.GetSubject(), opened.GetMessage(), opened.GetSpanId(), environmentTitle, unit.ID)
	}
	if len(opened.GetStarted().GetParentSpanId()) != 0 {
		t.Errorf("unit parent = %x, want none (a unit is a root)", opened.GetStarted().GetParentSpanId())
	}
	if got := opened.GetPhase(); got != progressv1.Phase_PHASE_PROVISION {
		t.Errorf("unit phase = %v, want the provision phase it runs in", got)
	}
	if StageID(nested.GetStarted().GetParentSpanId()) != unit.ID {
		t.Errorf("detail parent = %x, want the unit %x", nested.GetStarted().GetParentSpanId(), unit.ID)
	}
	if nested.GetMessage() != "dns records" || nested.GetSubject() != "production" || nested.GetPhase() != progressv1.Phase_PHASE_PROVISION {
		t.Errorf("the detail starts as %q: %q in %v, want its unit's \"production\": \"dns records\" in the provision phase",
			nested.GetSubject(), nested.GetMessage(), nested.GetPhase())
	}
	for i, event := range events {
		if err := protovalidate.Validate(event); err != nil {
			t.Errorf("started event %d fails the wire's own rules: %v", i, err)
		}
	}
}

func TestUnitIDsAreTheSharedNamingDigests(t *testing.T) {
	t.Parallel()

	stream := &recordingStream{}
	sender := newEventStream(context.Background(), stream.send)
	tracer := newEventTrace(sender)

	tracer.Start(time.Now(),
		environmentUnit(progressv1.Phase_PHASE_PROVISION),
		UnitStage(naming.UnitEdge, "cloudflare", "Reconciling the routes for shop in production", progressv1.Phase_PHASE_DEPLOY),
		UnitStage(naming.UnitPromotion, "production", "Switching traffic to promotion p1", progressv1.Phase_PHASE_PROMOTE),
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
			t.Errorf("stage %d id = %s, want the naming digest %s", i, got, want)
		}
		if len(events[i].GetSpanId()) != naming.StageIDLen {
			t.Errorf("stage %d id is %d bytes, want %d", i, len(events[i].GetSpanId()), naming.StageIDLen)
		}
	}
}

func TestDetailStagesMintTheirOwnIDUnderTheirUnit(t *testing.T) {
	t.Parallel()

	unit := UnitStage(naming.UnitPromotion, "production", "Switching traffic to promotion p1", progressv1.Phase_PHASE_PROMOTE)
	first := NewStage(unit, "detail")
	second := NewStage(unit, "detail")

	if first.ID == second.ID {
		t.Error("two detail stages share an id, want each minted on its own")
	}
	if first.ParentID != unit.ID {
		t.Error("a detail stage hangs off something other than its unit")
	}
}

func TestAnEndedScopeNamesItsStageEndsAtItsEndAndCarriesItsStartAndAttributes(t *testing.T) {
	t.Parallel()

	stream := &recordingStream{}
	sender := newEventStream(context.Background(), stream.send)
	tracer := newEventTrace(sender)

	root := environmentUnit(progressv1.Phase_PHASE_PROVISION)
	child := NewStage(root, "web")
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
	if StageID(event.GetSpanId()) != child.ID {
		t.Errorf("span id = %x, want the stage's id %x", event.GetSpanId(), child.ID)
	}
	if event.GetPhase() != progressv1.Phase_PHASE_PROVISION {
		t.Errorf("phase = %v, want the provision phase", event.GetPhase())
	}
	if ended.GetStatus() != progressv1.SpanStatus_SPAN_STATUS_OK {
		t.Errorf("status = %v, want OK", ended.GetStatus())
	}
	if ended.GetStartTimeUnixNano() != start.UnixNano() || event.GetTimeUnixNano() != end.UnixNano() {
		t.Errorf("times = %d/%d, want the scope's start %d and its end %d on the envelope", ended.GetStartTimeUnixNano(), event.GetTimeUnixNano(), start.UnixNano(), end.UnixNano())
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
	tracer := newEventTrace(sender)

	child := NewStage(environmentUnit(progressv1.Phase_PHASE_PROVISION), "create resource")
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

func TestAFailedScopeEndsWithAnErrorKindNeverRawText(t *testing.T) {
	t.Parallel()

	stream := &recordingStream{}
	sender := newEventStream(context.Background(), stream.send)
	tracer := newEventTrace(sender)

	secret := "postgres://user:hunter2@10.0.0.1:5432/db AKIAABCDEF1234567890"
	stage := environmentUnit(progressv1.Phase_PHASE_PROVISION)
	tracer.End(stage, time.Now(), time.Now(), errors.New(secret))

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
		t.Fatal("no ATTRIBUTE_KEY_ERROR_KIND attribute on a failed scope")
	}
	if strings.Contains(got, "hunter2") || strings.Contains(event.GetMessage(), "hunter2") {
		t.Fatal("the ended event carries the raw error text")
	}
	if got != provider.ErrorKindFailed {
		t.Errorf("ERROR_KIND = %q, want a bounded classification", got)
	}
}

func TestAFailedScopeEndsAtErrorLevelAndASucceededOneAtInfo(t *testing.T) {
	t.Parallel()

	stream := &recordingStream{}
	sender := newEventStream(context.Background(), stream.send)
	tracer := newEventTrace(sender)

	stage := environmentUnit(progressv1.Phase_PHASE_PROVISION)
	tracer.End(stage, time.Now(), time.Now(), errors.New("the stack refused"))
	tracer.End(stage, time.Now(), time.Now(), nil)

	if err := sender.close(); err != nil {
		t.Fatalf("close() error = %v", err)
	}
	events := stream.recorded()
	if got := events[0].GetLevel(); got != progressv1.Level_LEVEL_ERROR {
		t.Errorf("a failed scope ends at %v, want LEVEL_ERROR", got)
	}
	if got := events[1].GetLevel(); got != progressv1.Level_LEVEL_INFO {
		t.Errorf("a succeeded scope ends at %v, want LEVEL_INFO", got)
	}
}

func TestStageTitlesAreSanitized(t *testing.T) {
	t.Parallel()

	if got := UnitStage(naming.UnitEnvironment, "production", "\x1b[2J", progressv1.Phase_PHASE_PROVISION).Title; got != "[2J" {
		t.Errorf("UnitStage() title = %q, want the control characters gone", got)
	}
	if got := UnitStage(naming.UnitEnvironment, "production", "   ", progressv1.Phase_PHASE_PROVISION).Title; got != "stage" {
		t.Errorf("UnitStage() title = %q, want a fallback title", got)
	}
	if got := UnitStage(naming.UnitEnvironment, "production", strings.Repeat("a", maxStageTitleLen*2), progressv1.Phase_PHASE_PROVISION).Title; len(got) > maxStageTitleLen {
		t.Errorf("UnitStage() title is %d long, want it capped at %d", len(got), maxStageTitleLen)
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

func TestAFailedUnitSaysWhyAtErrorInItsUnitOnceBeforeTheUnitEnds(t *testing.T) {
	t.Parallel()

	stream := &recordingStream{}
	sender := newEventStream(context.Background(), stream.send)
	unit := UnitStage("web", "web", "Deploying the serverless app to production", progressv1.Phase_PHASE_DEPLOY)
	_ = newStageScope(sender).unit(unit, func(u *unitRun) error {
		return u.phase(func(progress.Progress) error {
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
	if StageID(reason.GetSpanId()) != unit.ID || reason.GetPhase() != progressv1.Phase_PHASE_DEPLOY {
		t.Errorf("the reason is scoped to %x in %v, want the unit %x in the deploy phase", reason.GetSpanId(), reason.GetPhase(), unit.ID)
	}
	if got, want := reason.GetMessage(), "the web stack could not be provisioned[0m"; got != want {
		t.Errorf("the reason reads %q, want the error sanitized like any message: %q", got, want)
	}
	at := slices.Index(events, reason)
	for _, event := range events[:at] {
		if event.GetEnded() != nil {
			t.Fatalf("a scope ended before the reason was said, want the reason inside its unit")
		}
	}
	for _, event := range events {
		if event.GetEnded() != nil && event.GetMessage() != "" {
			t.Errorf("an Ended carries %q, want Ended without text", event.GetMessage())
		}
	}
}

func TestAUnitsWorkSpeaksInTheUnitsOwnSpanWithNoSpanOfItsOwn(t *testing.T) {
	t.Parallel()

	stream := &recordingStream{}
	sender := newEventStream(context.Background(), stream.send)
	unit := UnitStage("web", "web", "Deploying the serverless app to production", progressv1.Phase_PHASE_DEPLOY)
	_ = newStageScope(sender).unit(unit, func(u *unitRun) error {
		return u.phase(func(progress progress.Progress) error {
			progress.Say("Uploading function web's artifact (1.2 MiB)")
			return nil
		})
	})

	if err := sender.close(); err != nil {
		t.Fatalf("close() error = %v", err)
	}
	var started int
	for _, event := range stream.recorded() {
		if StageID(event.GetSpanId()) != unit.ID {
			t.Errorf("an event is scoped to %x, want every one in the unit's span %x", event.GetSpanId(), unit.ID)
		}
		if event.GetStarted() != nil {
			started++
		}
	}
	if started != 1 {
		t.Errorf("%d spans started, want only the unit's", started)
	}
}

func TestAUnitThatFailsOutsideItsPhaseSaysWhyInItsOwnScope(t *testing.T) {
	t.Parallel()

	stream := &recordingStream{}
	sender := newEventStream(context.Background(), stream.send)
	unit := UnitStage("web", "web", "Deploying the serverless app to production", progressv1.Phase_PHASE_DEPLOY)
	_ = newStageScope(sender).unit(unit, func(*unitRun) error {
		return errors.New("the web stack is locked")
	})

	if err := sender.close(); err != nil {
		t.Fatalf("close() error = %v", err)
	}
	said := reasonsSaid(stream.recorded())
	if len(said) != 1 || StageID(said[0].GetSpanId()) != unit.ID || said[0].GetMessage() != "the web stack is locked" {
		t.Fatalf("said %d reasons, want the reason once in the unit's own scope", len(said))
	}
}

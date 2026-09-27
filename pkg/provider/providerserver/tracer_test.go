package providerserver

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"buf.build/go/protovalidate"

	"github.com/ocelhq/ocel/pkg/naming"

	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

func TestDeclaringStagesStartsEachOneTitledUnderItsParent(t *testing.T) {
	t.Parallel()

	stream := &recordingStream{}
	sender := newEventStream(context.Background(), stream.send)
	tracer := newEventTrace(sender)

	unit := UnitStage(naming.UnitEnvironment, "Environment")
	phase := PhaseStage(unit.Name, progressv1.Phase_PHASE_PROVISION)
	tracer.Start(time.Now(), unit, phase)

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
	if opened.GetMessage() != "Environment" || StageID(opened.GetSpanId()) != unit.ID {
		t.Errorf("the unit starts as %q %x, want \"Environment\" %x", opened.GetMessage(), opened.GetSpanId(), unit.ID)
	}
	if len(opened.GetStarted().GetParentSpanId()) != 0 {
		t.Errorf("unit parent = %x, want none (a unit is a root)", opened.GetStarted().GetParentSpanId())
	}
	if got := opened.GetPhase(); got != progressv1.Phase_PHASE_UNSPECIFIED {
		t.Errorf("unit phase = %v, want PHASE_UNSPECIFIED", got)
	}
	if StageID(nested.GetStarted().GetParentSpanId()) != unit.ID {
		t.Errorf("phase parent = %x, want the unit %x", nested.GetStarted().GetParentSpanId(), unit.ID)
	}
	if nested.GetMessage() != "Provisioning" || nested.GetPhase() != progressv1.Phase_PHASE_PROVISION {
		t.Errorf("the phase starts as %q in %v, want \"Provisioning\" in the provision phase", nested.GetMessage(), nested.GetPhase())
	}
	for i, event := range events {
		if err := protovalidate.Validate(event); err != nil {
			t.Errorf("started event %d fails the wire's own rules: %v", i, err)
		}
	}
}

func TestDeclaredUnitAndPhaseIDsAreTheSharedNamingDigests(t *testing.T) {
	t.Parallel()

	stream := &recordingStream{}
	sender := newEventStream(context.Background(), stream.send)
	tracer := newEventTrace(sender)

	unit := UnitStage(naming.UnitEnvironment, "Environment")
	tracer.Start(time.Now(),
		unit,
		PhaseStage(unit.Name, progressv1.Phase_PHASE_BUILD),
		PhaseStage(unit.Name, progressv1.Phase_PHASE_DEPLOY),
		PhaseStage(unit.Name, progressv1.Phase_PHASE_PROVISION),
		PhaseStage(unit.Name, progressv1.Phase_PHASE_PROMOTE),
		PhaseStage(unit.Name, progressv1.Phase_PHASE_DESTROY),
	)

	if err := sender.close(); err != nil {
		t.Fatalf("close() error = %v", err)
	}
	events := stream.recorded()
	for i, want := range []string{
		"9f2ecbbdfa2db89d",
		"4b5ac07b8124802c",
		"8b528c00a0fb6065",
		"ed0ca2aae3a67905",
		"92988f8d30813314",
		"7da3bb7483e4884a",
	} {
		if got := hex.EncodeToString(events[i].GetSpanId()); got != want {
			t.Errorf("stage %d id = %s, want the naming digest %s", i, got, want)
		}
		if len(events[i].GetSpanId()) != naming.StageIDLen {
			t.Errorf("stage %d id is %d bytes, want %d", i, len(events[i].GetSpanId()), naming.StageIDLen)
		}
	}
}

func TestDetailStagesMintTheirOwnIDUnderTheirPhase(t *testing.T) {
	t.Parallel()

	unit := UnitStage(naming.UnitPromotion, "Promotion")
	phase := PhaseStage(unit.Name, progressv1.Phase_PHASE_PROMOTE)
	first := NewStage(phase, "detail")
	second := NewStage(phase, "detail")

	if first.ID == second.ID {
		t.Error("two detail stages share an id, want each minted on its own")
	}
	if first.ParentID != phase.ID {
		t.Error("a detail stage hangs off something other than its phase")
	}
}

func TestAnEndedScopeNamesItsStageEndsAtItsEndAndCarriesItsStartAndAttributes(t *testing.T) {
	t.Parallel()

	stream := &recordingStream{}
	sender := newEventStream(context.Background(), stream.send)
	tracer := newEventTrace(sender)

	root := UnitStage(naming.UnitEnvironment, "Environment")
	child := NewStage(root, "web")
	start := time.Unix(1000, 0)
	end := time.Unix(1005, 0)
	tracer.End(child, progressv1.Phase_PHASE_PROVISION, start, end, nil, provider.AttrApp("web"), provider.AttrResourceCount(3))

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

func TestAFailedScopeEndsWithAnErrorKindNeverRawText(t *testing.T) {
	t.Parallel()

	stream := &recordingStream{}
	sender := newEventStream(context.Background(), stream.send)
	tracer := newEventTrace(sender)

	secret := "postgres://user:hunter2@10.0.0.1:5432/db AKIAABCDEF1234567890"
	stage := UnitStage(naming.UnitEnvironment, "Environment")
	tracer.End(stage, progressv1.Phase_PHASE_PROVISION, time.Now(), time.Now(), errors.New(secret))

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

func TestStageTitlesAreSanitized(t *testing.T) {
	t.Parallel()

	if got := UnitStage(naming.UnitEnvironment, "\x1b[2J").Title; got != "[2J" {
		t.Errorf("UnitStage() title = %q, want the control characters gone", got)
	}
	if got := UnitStage(naming.UnitEnvironment, "   ").Title; got != "stage" {
		t.Errorf("UnitStage() title = %q, want a fallback title", got)
	}
	if got := UnitStage(naming.UnitEnvironment, strings.Repeat("a", maxStageTitleLen*2)).Title; len(got) > maxStageTitleLen {
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

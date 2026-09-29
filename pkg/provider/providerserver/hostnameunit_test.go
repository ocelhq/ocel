package providerserver_test

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/refusal"
)

type hostnameUnit struct {
	ended    *progressv1.OperationEvent
	warnings []string
}

func hostnameUnitOf(t *testing.T, events []*progressv1.OperationEvent) hostnameUnit {
	t.Helper()
	var spans [][]byte
	var unit hostnameUnit
	for _, event := range events {
		started := event.GetStarted()
		switch {
		case started != nil && len(started.GetParentSpanId()) == 0 && strings.HasPrefix(event.GetMessage(), "Attaching "):
			spans = append(spans, event.GetSpanId())
		case started != nil && slices.ContainsFunc(spans, func(span []byte) bool { return bytes.Equal(span, started.GetParentSpanId()) }):
			spans = append(spans, event.GetSpanId())
		case !slices.ContainsFunc(spans, func(span []byte) bool { return bytes.Equal(span, event.GetSpanId()) }):
		case event.GetEnded() != nil && bytes.Equal(event.GetSpanId(), spans[0]):
			unit.ended = event
		case event.GetEnded() == nil && event.GetLevel() == progressv1.Level_LEVEL_WARN:
			unit.warnings = append(unit.warnings, event.GetMessage())
		}
	}
	if unit.ended == nil {
		t.Fatal("the deploy ended no unit attaching hostnames")
	}
	return unit
}

func TestAHostnameAnotherEdgeServesLeavesTheAttachingUnitAtAWarningNamingOnlyTheHostnamesItAttached(t *testing.T) {
	builtProject(t)
	client, _ := deployServed(t)

	req := deployRequest()
	req.Edge = writtenBy("shop.example")
	if result, _ := deploy(t, client, req); !result.GetSuccess() {
		t.Fatalf("Deploy() on the %s edge = %q", fake.KindRelay, result.GetError())
	}

	req.Edge.Kind = string(fake.KindDirect)
	req.Manifest.Domains[0].Hostnames = append(req.Manifest.Domains[0].Hostnames, "www.shop.example")
	result, events := deploy(t, client, req)
	if !result.GetSuccess() {
		t.Fatalf("Deploy() on the %s edge = %q", fake.KindDirect, result.GetError())
	}

	unit := hostnameUnitOf(t, events)
	if unit.ended.GetLevel() != progressv1.Level_LEVEL_WARN || unit.ended.GetEnded().GetStatus() != progressv1.SpanStatus_SPAN_STATUS_OK {
		t.Errorf("the unit ended %s at %s, want OK at WARN: the release succeeded, one hostname did not move", unit.ended.GetEnded().GetStatus(), unit.ended.GetLevel())
	}
	if want := "Attached production hostname www.shop.example but not shop.example"; unit.ended.GetMessage() != want {
		t.Errorf("the unit ended saying %q, want %q", unit.ended.GetMessage(), want)
	}
	if len(unit.warnings) != 1 || !strings.HasPrefix(unit.warnings[0], "shop.example is still served through the relay edge") {
		t.Errorf("the unit warned %q, want one warning naming shop.example and the edge still serving it", unit.warnings)
	}
}

func TestAHostnameWhoseCertificateIsStillIssuingLeavesTheAttachingUnitAtAWarning(t *testing.T) {
	builtProject(t)
	client, p := deployServed(t)
	p.RequireValidationRecords(edge.Record{Name: "_acme.shop.example", Type: edge.RecordTypeCNAME, Value: "validate.example"})
	p.StallAfterProving(provider.Resumable(refusal.Refuse(refusal.CodeNotReady, "the certificate is still validating")))

	req := deployRequest()
	req.Edge = writtenBy("shop.example")
	result, events := deploy(t, client, req)
	if !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}

	unit := hostnameUnitOf(t, events)
	if unit.ended.GetLevel() != progressv1.Level_LEVEL_WARN {
		t.Errorf("the unit ended at %s, want WARN: its one hostname is not served yet", unit.ended.GetLevel())
	}
	if want := "Did not attach production hostname shop.example"; unit.ended.GetMessage() != want {
		t.Errorf("the unit ended saying %q, want %q", unit.ended.GetMessage(), want)
	}
	if want := "shop.example is not served yet: the certificate is still validating"; !slices.Contains(unit.warnings, want) {
		t.Errorf("the unit warned %q, want %q among them", unit.warnings, want)
	}
}

func TestAHostnameUnitThatAttachesEveryHostnameEndsWithoutAWarning(t *testing.T) {
	builtProject(t)
	client, _ := deployServed(t)

	req := deployRequest()
	req.Edge = writtenBy("shop.example")
	result, events := deploy(t, client, req)
	if !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}
	unit := hostnameUnitOf(t, events)
	if unit.ended.GetLevel() != progressv1.Level_LEVEL_INFO || unit.ended.GetMessage() != "" || len(unit.warnings) != 0 {
		t.Errorf("the unit ended at %s saying %q after warning %q, want INFO, no message and no warning", unit.ended.GetLevel(), unit.ended.GetMessage(), unit.warnings)
	}
}

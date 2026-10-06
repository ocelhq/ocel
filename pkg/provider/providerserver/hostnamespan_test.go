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

type hostnameSpan struct {
	ended    *progressv1.OperationEvent
	warnings []string
}

func hostnameSpanOf(t *testing.T, events []*progressv1.OperationEvent) hostnameSpan {
	t.Helper()
	var spans [][]byte
	var root hostnameSpan
	for _, event := range events {
		started := event.GetStarted()
		switch {
		case started != nil && len(started.GetParentSpanId()) == 0 && strings.HasPrefix(event.GetMessage(), "Attaching "):
			spans = append(spans, event.GetSpanId())
		case started != nil && slices.ContainsFunc(spans, func(span []byte) bool { return bytes.Equal(span, started.GetParentSpanId()) }):
			spans = append(spans, event.GetSpanId())
		case !slices.ContainsFunc(spans, func(span []byte) bool { return bytes.Equal(span, event.GetSpanId()) }):
		case event.GetEnded() != nil && bytes.Equal(event.GetSpanId(), spans[0]):
			root.ended = event
		case event.GetEnded() == nil && event.GetLevel() == progressv1.Level_LEVEL_WARN:
			root.warnings = append(root.warnings, event.GetMessage())
		}
	}
	if root.ended == nil {
		t.Fatal("the deploy ended no span attaching hostnames")
	}
	return root
}

func TestAHostnameAnotherEdgeServesLeavesTheAttachingSpanAtAWarningNamingOnlyTheHostnamesItAttached(t *testing.T) {
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

	root := hostnameSpanOf(t, events)
	if root.ended.GetLevel() != progressv1.Level_LEVEL_WARN || root.ended.GetEnded().GetStatus() != progressv1.SpanStatus_SPAN_STATUS_OK {
		t.Errorf("the span ended %s at %s, want OK at WARN: the release succeeded, one hostname did not move", root.ended.GetEnded().GetStatus(), root.ended.GetLevel())
	}
	if want := "Attached production hostname www.shop.example but not shop.example"; root.ended.GetEnded().GetTitle() != want {
		t.Errorf("the span ended titled %q, want %q", root.ended.GetEnded().GetTitle(), want)
	}
	if len(root.warnings) != 1 || !strings.HasPrefix(root.warnings[0], "shop.example is still served through the relay edge") {
		t.Errorf("the span warned %q, want one warning naming shop.example and the edge still serving it", root.warnings)
	}
}

func TestAHostnameAWorkerRouteAlreadySendsToTheEdgeIsAttachedThroughItRatherThanReportedAsServedElsewhere(t *testing.T) {
	builtProject(t)
	client, p := deployServed(t)

	req := deployRequest()
	req.Edge = writtenBy("shop.example")
	req.Edge.Kind = string(fake.KindDirect)
	if result, _ := deploy(t, client, req); !result.GetSuccess() {
		t.Fatalf("Deploy() on the %s edge = %q", fake.KindDirect, result.GetError())
	}

	p.Edges().(*fake.Edges).RouteOnlyThrough(fake.KindRelay, "shop.example")
	req.Edge.Kind = string(fake.KindRelay)
	result, events := deploy(t, client, req)
	if !result.GetSuccess() {
		t.Fatalf("Deploy() on the %s edge = %q", fake.KindRelay, result.GetError())
	}

	root := hostnameSpanOf(t, events)
	if len(root.warnings) != 0 {
		t.Errorf("the span warned %q, want no warning: the edge already answers shop.example", root.warnings)
	}
	if want := "Attached production hostname shop.example"; root.ended.GetEnded().GetTitle() != want {
		t.Errorf("the span ended titled %q, want %q", root.ended.GetEnded().GetTitle(), want)
	}
	if !slices.ContainsFunc(p.Edges().(*fake.Edges).Edge(fake.KindRelay).Bindings(), func(b edge.DomainBinding) bool { return b.Hostname == "shop.example" }) {
		t.Errorf("the %s edge bound %v, want shop.example among them", fake.KindRelay, p.Edges().(*fake.Edges).Edge(fake.KindRelay).Bindings())
	}
	if root.ended.GetLevel() == progressv1.Level_LEVEL_WARN || root.ended.GetEnded().GetStatus() != progressv1.SpanStatus_SPAN_STATUS_OK {
		t.Errorf("the span ended %s at %s, want OK below WARN: the hostname moved cleanly", root.ended.GetEnded().GetStatus(), root.ended.GetLevel())
	}
}

func TestAHostnameStillAnsweredThroughAnotherFrontKeepsTheNoteThatDomainAddMovesIt(t *testing.T) {
	builtProject(t)
	client, _ := deployServed(t)

	req := deployRequest()
	req.Edge = writtenBy("shop.example")
	req.Edge.Kind = string(fake.KindDirect)
	if result, _ := deploy(t, client, req); !result.GetSuccess() {
		t.Fatalf("Deploy() on the %s edge = %q", fake.KindDirect, result.GetError())
	}

	req.Edge.Kind = string(fake.KindRelay)
	result, events := deploy(t, client, req)
	if !result.GetSuccess() {
		t.Fatalf("Deploy() on the %s edge = %q", fake.KindRelay, result.GetError())
	}

	root := hostnameSpanOf(t, events)
	if want := "Did not attach production hostname shop.example"; root.ended.GetEnded().GetTitle() != want {
		t.Errorf("the span ended titled %q, want %q", root.ended.GetEnded().GetTitle(), want)
	}
	if len(root.warnings) != 1 || !strings.HasPrefix(root.warnings[0], "shop.example is still served through the direct edge") {
		t.Errorf("the span warned %q, want one warning naming shop.example and the direct edge", root.warnings)
	}
}

func TestAHostnameWhoseCertificateIsStillIssuingLeavesTheAttachingSpanAtAWarning(t *testing.T) {
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

	root := hostnameSpanOf(t, events)
	if root.ended.GetLevel() != progressv1.Level_LEVEL_WARN {
		t.Errorf("the span ended at %s, want WARN: its one hostname is not served yet", root.ended.GetLevel())
	}
	if want := "Did not attach production hostname shop.example"; root.ended.GetEnded().GetTitle() != want {
		t.Errorf("the span ended titled %q, want %q", root.ended.GetEnded().GetTitle(), want)
	}
	if want := "shop.example is not served yet: the certificate is still validating"; !slices.Contains(root.warnings, want) {
		t.Errorf("the span warned %q, want %q among them", root.warnings, want)
	}
}

func TestAHostnameSpanThatAttachesEveryHostnameEndsWithoutAWarning(t *testing.T) {
	builtProject(t)
	client, _ := deployServed(t)

	req := deployRequest()
	req.Edge = writtenBy("shop.example")
	result, events := deploy(t, client, req)
	if !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}
	root := hostnameSpanOf(t, events)
	title := root.ended.GetEnded().GetTitle()
	if root.ended.GetLevel() != progressv1.Level_LEVEL_INFO || title != "Attached production hostname shop.example" || len(root.warnings) != 0 {
		t.Errorf("the span ended at %s titled %q after warning %q, want INFO, the finished title and no warning", root.ended.GetLevel(), title, root.warnings)
	}
}

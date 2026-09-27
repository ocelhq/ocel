package pulumi

import (
	"errors"
	"strings"
	"time"

	"github.com/pulumi/pulumi/sdk/v3/go/auto/events"
	"github.com/pulumi/pulumi/sdk/v3/go/common/apitype"
	"github.com/pulumi/pulumi/sdk/v3/go/common/diag/colors"

	"github.com/ocelhq/ocel/pkg/provider"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

var errResourceOperationFailed = errors.New("resource operation failed")

const (
	resourceLatencyOutlierThreshold = 30 * time.Second
	engineDrainGrace                = 30 * time.Second
	maxSlowOps                      = 20
	maxResourceIdentifierLen        = 256
	engineBatchSpanName             = "pulumi resource operations"
	maxDiagnosticLen                = 2048
	diagnosticSeverityError         = "error"
	redactedSecret                  = "[secret]"
)

type resourceOp struct {
	Op     apitype.OpType
	Action provider.ChangeAction
	Type   string
	Name   string
	Start  time.Time
	End    time.Time
	Failed bool

	Diagnostic string
}

type engineTrace struct {
	ResourceCount  int
	Start          time.Time
	End            time.Time
	Failed         bool
	Ops            []resourceOp
	SlowOpsDropped int
}

type inflightOp struct {
	typ   string
	start time.Time
}

type traceCollector struct {
	threshold   time.Duration
	secrets     []string
	inflight    map[string]inflightOp
	trace       engineTrace
	slowest     []resourceOp
	failedAt    map[string]int
	diagnostics map[string][]string
}

func newTraceCollector(threshold time.Duration, secrets []string) *traceCollector {
	return &traceCollector{
		threshold:   threshold,
		secrets:     secrets,
		inflight:    map[string]inflightOp{},
		failedAt:    map[string]int{},
		diagnostics: map[string][]string{},
	}
}

func (b *traceCollector) consume(ev events.EngineEvent, now time.Time) {
	if b.trace.Start.IsZero() {
		b.trace.Start = now
	}
	b.trace.End = now

	switch {
	case ev.ResourcePreEvent != nil:
		m := ev.ResourcePreEvent.Metadata
		b.inflight[m.URN] = inflightOp{typ: m.Type, start: now}
	case ev.ResOutputsEvent != nil:
		b.finish(ev.ResOutputsEvent.Metadata, now, false)
	case ev.ResOpFailedEvent != nil:
		b.trace.Failed = true
		b.finish(ev.ResOpFailedEvent.Metadata, now, true)
	case ev.DiagnosticEvent != nil:
		b.diagnose(ev.DiagnosticEvent)
	}
}

func (b *traceCollector) diagnose(d *apitype.DiagnosticEvent) {
	if d.Severity != diagnosticSeverityError || d.URN == "" || d.Ephemeral {
		return
	}
	if text := b.plainDiagnostic(d.Message); text != "" {
		b.diagnostics[d.URN] = append(b.diagnostics[d.URN], text)
	}
}

func (b *traceCollector) plainDiagnostic(message string) string {
	text := maskSecrets(colors.Never.Colorize(message), b.secrets)
	text = strings.Join(strings.Fields(text), " ")
	if len(text) > maxDiagnosticLen {
		text = strings.ToValidUTF8(text[:maxDiagnosticLen], "") + "…"
	}
	return text
}

func (b *traceCollector) finish(m apitype.StepEventMetadata, now time.Time, failed bool) {
	b.trace.ResourceCount++

	op, ok := b.inflight[m.URN]
	start := now
	if ok {
		start = op.start
		delete(b.inflight, m.URN)
	}

	action, changed := performedAction(m, failed)
	s := resourceOp{
		Op:     m.Op,
		Action: action,
		Type:   capIdentifier(m.Type),
		Name:   resourceNameFromURN(m.URN),
		Start:  start,
		End:    now,
		Failed: failed,
	}
	if failed {
		b.failedAt[m.URN] = len(b.trace.Ops)
	}
	if failed || changed {
		b.trace.Ops = append(b.trace.Ops, s)
		return
	}
	if b.threshold > 0 && now.Sub(start) >= b.threshold {
		b.keepSlowest(s)
	}
}

var performedActions = map[apitype.OpType]provider.ChangeAction{
	apitype.OpCreate:            provider.ActionCreate,
	apitype.OpImport:            provider.ActionCreate,
	apitype.OpUpdate:            provider.ActionUpdate,
	apitype.OpReplace:           provider.ActionReplace,
	apitype.OpImportReplacement: provider.ActionReplace,
	apitype.OpDelete:            provider.ActionDelete,
}

func performedAction(m apitype.StepEventMetadata, failed bool) (provider.ChangeAction, bool) {
	if m.Type == stackResourceType {
		return "", false
	}
	if failed && replacementSteps[m.Op] {
		return provider.ActionReplace, true
	}
	action, changed := performedActions[m.Op]
	return action, changed
}

var replacementSteps = map[apitype.OpType]bool{
	apitype.OpCreateReplacement: true,
	apitype.OpDeleteReplaced:    true,
	apitype.OpDiscardReplaced:   true,
}

func (b *traceCollector) keepSlowest(s resourceOp) {
	if len(b.slowest) < maxSlowOps {
		b.slowest = append(b.slowest, s)
		return
	}
	minIdx, minDur := 0, b.slowest[0].End.Sub(b.slowest[0].Start)
	for i := 1; i < len(b.slowest); i++ {
		if d := b.slowest[i].End.Sub(b.slowest[i].Start); d < minDur {
			minIdx, minDur = i, d
		}
	}
	b.trace.SlowOpsDropped++
	if d := s.End.Sub(s.Start); d > minDur {
		b.slowest[minIdx] = s
	}
}

func (b *traceCollector) result() engineTrace {
	for urn, i := range b.failedAt {
		b.trace.Ops[i].Diagnostic = strings.Join(b.diagnostics[urn], "; ")
	}
	b.trace.Ops = append(b.trace.Ops, b.slowest...)
	return b.trace
}

func capIdentifier(s string) string {
	if len(s) <= maxResourceIdentifierLen {
		return s
	}
	return strings.ToValidUTF8(s[:maxResourceIdentifierLen], "")
}

const (
	urnPrefix        = "urn:pulumi:"
	urnPartDelimiter = "::"
)

func resourceNameFromURN(raw string) string {
	if !strings.HasPrefix(raw, urnPrefix) {
		return ""
	}
	parts := strings.Split(raw, urnPartDelimiter)
	if len(parts) < 4 {
		return ""
	}
	return capIdentifier(strings.Join(parts[3:], urnPartDelimiter))
}

func drainTrace(engineEvents <-chan events.EngineEvent, threshold time.Duration, secrets []string) <-chan engineTrace {
	result := make(chan engineTrace, 1)
	go func() {
		b := newTraceCollector(threshold, secrets)
		for ev := range engineEvents {
			b.consume(ev, time.Now())
		}
		result <- b.result()
	}()
	return result
}

func awaitTrace(result <-chan engineTrace, grace time.Duration) engineTrace {
	select {
	case trace := <-result:
		return trace
	case <-time.After(grace):
		return engineTrace{}
	}
}

func reportTrace(progress edge.Progress, trace engineTrace, runErr error) {
	if progress == nil || (trace.ResourceCount == 0 && runErr == nil) {
		return
	}
	batchErr := runErr
	if batchErr == nil && trace.Failed {
		batchErr = errResourceOperationFailed
	}
	progress.Span(engineBatchSpanName, trace.Start, trace.End, batchErr, provider.AttrResourceCount(trace.ResourceCount))

	for _, s := range trace.Ops {
		var opErr error
		if s.Failed {
			opErr = errResourceOperationFailed
		}
		attrs := []edge.Attr{provider.AttrDurationMS(s.End.Sub(s.Start))}
		if s.Type != "" {
			attrs = append(attrs, provider.AttrResourceType(s.Type))
		}
		if s.Name != "" {
			attrs = append(attrs, provider.AttrResourceName(s.Name))
		}
		if s.Action != "" {
			attrs = append(attrs, provider.AttrResourceAction(s.Action))
		}
		progress.Span(resourceOpName(s.Op, s.Failed), s.Start, s.End, opErr, attrs...)
		if s.Failed && s.Diagnostic != "" {
			progress.Error(s.label() + ": " + s.Diagnostic)
		}
	}
}

func (s resourceOp) label() string {
	switch {
	case s.Name != "" && s.Type != "":
		return s.Name + " (" + s.Type + ")"
	case s.Name != "":
		return s.Name
	case s.Type != "":
		return s.Type
	default:
		return "a resource"
	}
}

func resourceOpName(op apitype.OpType, failed bool) string {
	if failed {
		return "resource operation failed"
	}
	switch op {
	case apitype.OpCreate, apitype.OpCreateReplacement, apitype.OpImport, apitype.OpImportReplacement:
		return "create resource"
	case apitype.OpUpdate:
		return "update resource"
	case apitype.OpDelete, apitype.OpDeleteReplaced, apitype.OpDiscardReplaced, apitype.OpReadDiscard:
		return "delete resource"
	case apitype.OpReplace:
		return "replace resource"
	case apitype.OpRead, apitype.OpReadReplacement:
		return "read resource"
	case apitype.OpRefresh:
		return "refresh resource"
	default:
		return "resource operation"
	}
}

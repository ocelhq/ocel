package pulumi

import (
	"errors"
	"strings"
	"time"

	"github.com/pulumi/pulumi/sdk/v3/go/auto/events"
	"github.com/pulumi/pulumi/sdk/v3/go/common/apitype"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

var errResourceOperationFailed = errors.New("resource operation failed")

const (
	resourceLatencyOutlierThreshold = 30 * time.Second
	engineDrainGrace                = 30 * time.Second
	maxLatencyStandouts             = 20
	maxResourceIdentifierLen        = 256
	engineBatchSpanName             = "pulumi resource operations"
)

type standout struct {
	Op     apitype.OpType
	Type   string
	Name   string
	Start  time.Time
	End    time.Time
	Failed bool
}

type engineTrace struct {
	ResourceCount    int
	Start            time.Time
	End              time.Time
	Failed           bool
	Standouts        []standout
	StandoutsDropped int
}

type inflightOp struct {
	typ   string
	start time.Time
}

type traceBuilder struct {
	threshold time.Duration
	inflight  map[string]inflightOp
	trace     engineTrace
	slowest   []standout
}

func newTraceBuilder(threshold time.Duration) *traceBuilder {
	return &traceBuilder{threshold: threshold, inflight: map[string]inflightOp{}}
}

func (b *traceBuilder) consume(ev events.EngineEvent, now time.Time) {
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
	}
}

func (b *traceBuilder) finish(m apitype.StepEventMetadata, now time.Time, failed bool) {
	b.trace.ResourceCount++

	op, ok := b.inflight[m.URN]
	start := now
	if ok {
		start = op.start
		delete(b.inflight, m.URN)
	}

	s := standout{
		Op:     m.Op,
		Type:   capIdentifier(m.Type),
		Name:   resourceNameFromURN(m.URN),
		Start:  start,
		End:    now,
		Failed: failed,
	}
	if failed {
		b.trace.Standouts = append(b.trace.Standouts, s)
		return
	}
	if b.threshold > 0 && now.Sub(start) >= b.threshold {
		b.keepSlowest(s)
	}
}

func (b *traceBuilder) keepSlowest(s standout) {
	if len(b.slowest) < maxLatencyStandouts {
		b.slowest = append(b.slowest, s)
		return
	}
	minIdx, minDur := 0, b.slowest[0].End.Sub(b.slowest[0].Start)
	for i := 1; i < len(b.slowest); i++ {
		if d := b.slowest[i].End.Sub(b.slowest[i].Start); d < minDur {
			minIdx, minDur = i, d
		}
	}
	b.trace.StandoutsDropped++
	if d := s.End.Sub(s.Start); d > minDur {
		b.slowest[minIdx] = s
	}
}

func (b *traceBuilder) result() engineTrace {
	b.trace.Standouts = append(b.trace.Standouts, b.slowest...)
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

func drainTrace(engineEvents <-chan events.EngineEvent, threshold time.Duration) <-chan engineTrace {
	result := make(chan engineTrace, 1)
	go func() {
		b := newTraceBuilder(threshold)
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

func reportTrace(report providerkit.Reporter, trace engineTrace, runErr error) {
	if report == nil || (trace.ResourceCount == 0 && runErr == nil) {
		return
	}
	batchErr := runErr
	if batchErr == nil && trace.Failed {
		batchErr = errResourceOperationFailed
	}
	report.Span(engineBatchSpanName, trace.Start, trace.End, batchErr, providerkit.AttrResourceCount(trace.ResourceCount))

	for _, s := range trace.Standouts {
		var standoutErr error
		if s.Failed {
			standoutErr = errResourceOperationFailed
		}
		attrs := []providerkit.Attr{providerkit.AttrDurationMS(s.End.Sub(s.Start))}
		if s.Type != "" {
			attrs = append(attrs, providerkit.AttrResourceType(s.Type))
		}
		if s.Name != "" {
			attrs = append(attrs, providerkit.AttrResourceName(s.Name))
		}
		report.Span(standoutName(s.Op, s.Failed), s.Start, s.End, standoutErr, attrs...)
	}
}

func standoutName(op apitype.OpType, failed bool) string {
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

package deploy

import (
	"errors"
	"strings"
	"time"

	"github.com/pulumi/pulumi/sdk/v3/go/auto/events"
	"github.com/pulumi/pulumi/sdk/v3/go/common/apitype"
)

var errEngineTraceFailed = errors.New("resource operation failed")

type ResourceStandout struct {
	Op     apitype.OpType
	Type   string
	Name   string
	Start  time.Time
	End    time.Time
	Failed bool
}

const maxResourceIdentifierLen = 256

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

type EngineTrace struct {
	ResourceCount    int
	Start            time.Time
	End              time.Time
	Failed           bool
	Standouts        []ResourceStandout
	StandoutsDropped int
}

type inflightOp struct {
	typ   string
	start time.Time
}

const maxLatencyStandouts = 20

type engineTraceBuilder struct {
	threshold time.Duration
	inflight  map[string]inflightOp
	trace     EngineTrace
	slowest   []ResourceStandout
}

func newEngineTraceBuilder(threshold time.Duration) *engineTraceBuilder {
	return &engineTraceBuilder{
		threshold: threshold,
		inflight:  map[string]inflightOp{},
	}
}

func (b *engineTraceBuilder) consume(ev events.EngineEvent, now time.Time) {
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

func (b *engineTraceBuilder) finish(m apitype.StepEventMetadata, now time.Time, failed bool) {
	b.trace.ResourceCount++

	op, ok := b.inflight[m.URN]
	start := now
	if ok {
		start = op.start
		delete(b.inflight, m.URN)
	}

	standout := ResourceStandout{
		Op:     m.Op,
		Type:   capIdentifier(m.Type),
		Name:   resourceNameFromURN(m.URN),
		Start:  start,
		End:    now,
		Failed: failed,
	}
	if failed {
		b.trace.Standouts = append(b.trace.Standouts, standout)
		return
	}
	if b.threshold > 0 && now.Sub(start) >= b.threshold {
		b.keepSlowest(standout)
	}
}

func (b *engineTraceBuilder) keepSlowest(s ResourceStandout) {
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

func (b *engineTraceBuilder) result() EngineTrace {
	b.trace.Standouts = append(b.trace.Standouts, b.slowest...)
	return b.trace
}

const engineBatchSpanName = "pulumi resource operations"

func resourceStandoutName(op apitype.OpType, failed bool) string {
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

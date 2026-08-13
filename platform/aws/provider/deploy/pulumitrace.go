package deploy

import (
	"errors"
	"time"

	"github.com/pulumi/pulumi/sdk/v3/go/auto/events"
	"github.com/pulumi/pulumi/sdk/v3/go/common/apitype"
)

// errEngineTraceFailed drives ClassifyError/SPAN_STATUS_ERROR for a
// resource operation the engine reported as failed. It is never surfaced
// to a caller and carries no text of its own — the failing resource's
// real error already went to the deploy's own error return.
var errEngineTraceFailed = errors.New("resource operation failed")

// ResourceStandout is one resource operation worth its own span: a failure,
// or an operation slower than the outlier threshold. Only what we ourselves
// control is kept — the Pulumi op and resource-type token, never the
// resource's URN, its logical name, or any diagnostic text the engine
// emitted alongside it.
type ResourceStandout struct {
	Op     apitype.OpType
	Type   string
	Start  time.Time
	End    time.Time
	Failed bool
}

// EngineTrace summarizes one Pulumi Automation API operation's structured
// engine events: a single count/duration rollup for the whole batch, plus
// the individual resource operations worth surfacing on their own.
type EngineTrace struct {
	ResourceCount int
	Start         time.Time
	End           time.Time
	Failed        bool
	Standouts     []ResourceStandout
}

type inflightOp struct {
	typ   string
	start time.Time
}

// engineTraceBuilder consumes a Pulumi Automation API EngineEvents stream
// and synthesises an EngineTrace from it. It never retains a resource's URN
// or any diagnostic message text past the call to consume that carried it.
type engineTraceBuilder struct {
	threshold time.Duration
	inflight  map[string]inflightOp
	trace     EngineTrace
}

func newEngineTraceBuilder(threshold time.Duration) *engineTraceBuilder {
	return &engineTraceBuilder{
		threshold: threshold,
		inflight:  map[string]inflightOp{},
	}
}

// consume folds one engine event into the trace being built. now is the
// event's observed time, passed in rather than read from the clock so the
// builder is deterministic under test.
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

	outlier := !failed && b.threshold > 0 && now.Sub(start) >= b.threshold
	if failed || outlier {
		b.trace.Standouts = append(b.trace.Standouts, ResourceStandout{
			Op:     m.Op,
			Type:   m.Type,
			Start:  start,
			End:    now,
			Failed: failed,
		})
	}
}

func (b *engineTraceBuilder) result() EngineTrace {
	return b.trace
}

// engineBatchSpanName is the span name for the rollup of one Pulumi
// Automation API operation's resource changes.
const engineBatchSpanName = "pulumi resource operations"

// resourceStandoutName is deliberately drawn only from our own bounded
// vocabulary, keyed on the Pulumi op. It never includes the resource's
// type token, logical name, or URN: SpanEvent.name is free-form, and the
// wire format gives a failed resource operation no field of its own to
// carry an identifier in, so nothing dynamic is templated into it.
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

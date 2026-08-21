package pulumi

import (
	"errors"
	"strings"
	"time"

	"github.com/pulumi/pulumi/sdk/v3/go/auto/events"
	"github.com/pulumi/pulumi/sdk/v3/go/common/apitype"
)

// errOperationFailed marks a span whose own operation failed. A failed run is
// often reported by the engine as one summary error, and without this the trace
// would show every span green under a red stage.
var errOperationFailed = errors.New("resource operation failed")

// latencyOutlierThreshold is what makes an operation worth a span of its own. A
// deploy makes hundreds of resources and almost all of them are instant; the
// ones above this are the ones that explain the wall clock.
const latencyOutlierThreshold = 30 * time.Second

// drainGrace bounds how long a finished run waits for the event stream to close.
// A trace is never worth holding a deploy open for.
const drainGrace = 30 * time.Second

// maxStandouts caps what one run contributes to a trace. Slow operations are
// kept over fast ones because a trace that drops its worst entries answers
// nothing.
const maxStandouts = 20

const batchSpanName = "pulumi resource operations"

// standout is one operation the trace keeps: every failure, plus the slowest of
// whatever crossed the threshold.
type standout struct {
	Op     apitype.OpType
	Type   string
	Name   string
	Start  time.Time
	End    time.Time
	Failed bool
}

type engineTrace struct {
	ResourceCount int
	Start         time.Time
	End           time.Time
	Failed        bool
	Standouts     []standout
}

// startTraceDrain consumes the engine's stream in the background, because the
// engine blocks on a full channel and a deploy must never be slowed by its own
// telemetry.
func startTraceDrain(stream <-chan events.EngineEvent, threshold time.Duration) <-chan engineTrace {
	result := make(chan engineTrace, 1)
	go func() {
		b := newTraceBuilder(threshold)
		for ev := range stream {
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

// consume times operations from the engine's own pre/post pair rather than from
// any duration the engine reports, so a step that never completes still has a
// start the failure span can use.
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
	if len(b.slowest) < maxStandouts {
		b.slowest = append(b.slowest, s)
		return
	}
	minIdx, minDur := 0, b.slowest[0].End.Sub(b.slowest[0].Start)
	for i := 1; i < len(b.slowest); i++ {
		if d := b.slowest[i].End.Sub(b.slowest[i].Start); d < minDur {
			minIdx, minDur = i, d
		}
	}
	if d := s.End.Sub(s.Start); d > minDur {
		b.slowest[minIdx] = s
	}
}

func (b *traceBuilder) result() engineTrace {
	b.trace.Standouts = append(b.trace.Standouts, b.slowest...)
	return b.trace
}

const (
	urnPrefix        = "urn:pulumi:"
	urnPartDelimiter = "::"
)

// maxIdentifierLen bounds what a trace carries, since a URN's tail is a user's
// own string and a trace backend is not the place to discover its limits.
const maxIdentifierLen = 256

// resourceNameFromURN keeps the part of a URN that identifies the resource to a
// human: everything after the stack and project the span already sits under.
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

func capIdentifier(s string) string {
	if len(s) <= maxIdentifierLen {
		return s
	}
	return strings.ToValidUTF8(s[:maxIdentifierLen], "")
}

// standoutName reads the engine's op as a verb, because a trace is read by
// someone asking what took the time, not what the engine's enum is called.
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

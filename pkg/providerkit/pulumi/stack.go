package pulumi

import (
	"context"
	"strconv"
	"time"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/auto/events"
	"github.com/pulumi/pulumi/sdk/v3/go/auto/optup"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

// parallelism is high because the engine's own dependency graph is the real
// limit; a lower number only serialises work that has no reason to be ordered.
const parallelism = 64

// eventBuffer absorbs the burst a large stack emits so the engine is never
// blocked writing to a channel the trace builder has not drained yet.
const eventBuffer = 256

func (a *Adapter) up(ctx context.Context, stack auto.Stack, report providerkit.Reporter) (auto.UpResult, error) {
	lines := lineWriter(report.Detail)
	stream := make(chan events.EngineEvent, eventBuffer)
	traced := startTraceDrain(stream, latencyOutlierThreshold)

	start := time.Now()
	res, err := stack.Up(ctx,
		optup.Parallel(parallelism),
		optup.ProgressStreams(lines),
		optup.EventStreams(stream),
	)
	end := time.Now()
	lines.Flush()

	trace := awaitTrace(traced, drainGrace)
	if trace.Start.IsZero() {
		trace.Start, trace.End = start, end
	}
	emitTrace(report, trace, err)
	return res, err
}

// emitTrace hands the engine's operations to the kit as spans. The kit never
// learns what an engine event is; it gets timings under the stage it is already
// running, which is all a trace needs to show where a deploy's minutes went.
func emitTrace(report providerkit.Reporter, trace engineTrace, upErr error) {
	if trace.ResourceCount == 0 && upErr == nil {
		return
	}
	batchErr := upErr
	if batchErr == nil && trace.Failed {
		batchErr = errOperationFailed
	}
	report.Span(batchSpanName, trace.Start, trace.End, batchErr,
		providerkit.Attr{Key: providerkit.AttrResourceCount, Value: strconv.Itoa(trace.ResourceCount)})

	for _, s := range trace.Standouts {
		var err error
		if s.Failed {
			err = errOperationFailed
		}
		var attrs []providerkit.Attr
		if s.Type != "" {
			attrs = append(attrs, providerkit.Attr{Key: providerkit.AttrResourceType, Value: s.Type})
		}
		if s.Name != "" {
			attrs = append(attrs, providerkit.Attr{Key: providerkit.AttrResourceName, Value: s.Name})
		}
		report.Span(standoutName(s.Op, s.Failed), s.Start, s.End, err, attrs...)
	}
}

// reporterOr keeps every call site free of nil checks. Inspect has no Reporter
// at all, and a caller is allowed to pass none.
func reporterOr(r providerkit.Reporter) providerkit.Reporter {
	if r == nil {
		return silent{}
	}
	return r
}

type silent struct{}

func (silent) Say(string)    {}
func (silent) Detail(string) {}

func (silent) Span(string, time.Time, time.Time, error, ...providerkit.Attr) {}

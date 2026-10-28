package pulumi

import (
	"context"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/pulumi/pulumi/sdk/v3/go/auto/events"
	"github.com/pulumi/pulumi/sdk/v3/go/common/apitype"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
)

const errUnfinishedStep = "the engine stream closed before this step reported an outcome"

type resourceStep struct {
	name  string
	span  trace.Span
	start time.Time
}

type resourceSteps struct {
	ctx         context.Context
	secrets     []string
	open        map[string]resourceStep
	started     map[string]trace.SpanContext
	diagnostics map[string][]string
	progress    map[string]trace.Span
	finished    int
	ops         []resourceOp
}

func newResourceSteps(ctx context.Context, secrets []string) *resourceSteps {
	return &resourceSteps{
		ctx:         ctx,
		secrets:     secrets,
		open:        map[string]resourceStep{},
		started:     map[string]trace.SpanContext{},
		diagnostics: map[string][]string{},
		progress:    map[string]trace.Span{},
	}
}

func (s *resourceSteps) consume(event events.EngineEvent, now time.Time) {
	switch {
	case event.ResourcePreEvent != nil:
		s.begin(event.ResourcePreEvent.Metadata, event.ResourcePreEvent.Planning, now)
	case event.ResOutputsEvent != nil:
		s.finish(event.ResOutputsEvent.Metadata, now, nil)
	case event.ResOpFailedEvent != nil:
		s.finish(event.ResOpFailedEvent.Metadata, now, event.ResOpFailedEvent)
	case event.DiagnosticEvent != nil:
		s.diagnose(event.DiagnosticEvent, now)
	case event.ProgressEvent != nil:
		s.advance(event.ProgressEvent, now)
	case event.SummaryEvent != nil:
		s.summarize(event.SummaryEvent)
	case event.CancelEvent != nil:
		trace.SpanFromContext(s.ctx).AddEvent("cancel", trace.WithTimestamp(now))
	}
}

func (s *resourceSteps) begin(m apitype.StepEventMetadata, planning bool, now time.Time) resourceStep {
	parent := s.ctx
	if parentSpan, ok := s.started[parentURN(m)]; ok {
		parent = trace.ContextWithSpanContext(s.ctx, parentSpan)
	}
	step := resourceStep{name: stepName(m.Op), start: now}
	_, step.span = tracer.Start(parent, step.name, trace.WithTimestamp(now), trace.WithAttributes(stepAttributes(m)...))
	if planning {
		step.span.SetAttributes(attribute.Bool("pulumi.step.planning", true))
	}
	if previous, ok := s.open[m.URN]; ok {
		previous.span.End(trace.WithTimestamp(now))
	}
	s.open[m.URN] = step
	s.started[m.URN] = step.span.SpanContext()
	return step
}

func (s *resourceSteps) finish(m apitype.StepEventMetadata, now time.Time, failure *apitype.ResOpFailedEvent) {
	step, ok := s.open[m.URN]
	if !ok {
		if _, seen := s.started[m.URN]; seen && failure == nil {
			return
		}
		step = s.begin(m, false, now)
	}
	delete(s.open, m.URN)
	s.finished++
	if id := stepResourceID(m); id != "" {
		step.span.SetAttributes(attribute.String("pulumi.resource.id", id))
	}
	if action, changed := performedAction(m, failure != nil); changed || failure != nil {
		s.record(step, m, action, now, failure != nil)
	}
	if failure != nil {
		step.span.SetAttributes(attribute.Int("pulumi.step.status", failure.Status))
		message := strings.Join(s.diagnostics[m.URN], "; ")
		if message == "" {
			message = errResourceOperationFailed.Error()
		}
		step.span.SetStatus(codes.Error, message)
	}
	step.span.End(trace.WithTimestamp(now))
}

func (s *resourceSteps) record(step resourceStep, m apitype.StepEventMetadata, action provider.ChangeAction, now time.Time, failed bool) {
	op := resourceOp{
		Action:     action,
		Type:       capIdentifier(m.Type),
		Name:       resourceNameFromURN(m.URN),
		Start:      step.start,
		End:        now,
		Failed:     failed,
		Diagnostic: strings.Join(s.diagnostics[m.URN], "; "),
	}
	s.ops = append(s.ops, op)
	attrs := []attribute.KeyValue{attribute.String(progress.AttrKeySpanName.Name, step.name)}
	if op.Type != "" {
		attrs = append(attrs, attribute.String(progress.AttrKeyResourceType.Name, op.Type))
	}
	if op.Name != "" {
		attrs = append(attrs, attribute.String(progress.AttrKeyResourceName.Name, op.Name))
	}
	if op.Action != "" {
		attrs = append(attrs, attribute.String(progress.AttrKeyResourceAction.Name, string(op.Action)))
	}
	step.span.SetAttributes(attrs...)
}

func (s *resourceSteps) diagnose(d *apitype.DiagnosticEvent, now time.Time) {
	if d.Ephemeral {
		return
	}
	text := plainText(d.Message, s.secrets)
	if text == "" {
		return
	}
	target := trace.SpanFromContext(s.ctx)
	if step, ok := s.open[d.URN]; ok && d.URN != "" {
		target = step.span
	}
	target.AddEvent("diagnostic", trace.WithTimestamp(now), trace.WithAttributes(
		attribute.String("pulumi.diagnostic.severity", d.Severity),
		attribute.String("pulumi.diagnostic.message", text),
	))
	if d.Severity == diagnosticSeverityError && d.URN != "" {
		s.diagnostics[d.URN] = append(s.diagnostics[d.URN], text)
	}
}

func (s *resourceSteps) advance(p *apitype.ProgressEvent, now time.Time) {
	span, ok := s.progress[p.ID]
	if !ok {
		_, span = tracer.Start(s.ctx, progressName(p.Type), trace.WithTimestamp(now),
			trace.WithAttributes(attribute.String("pulumi.progress.message", p.Message)))
		s.progress[p.ID] = span
	}
	if !p.Done {
		return
	}
	span.SetAttributes(
		attribute.Int64("pulumi.progress.completed", p.Completed),
		attribute.Int64("pulumi.progress.total", p.Total),
	)
	span.End(trace.WithTimestamp(now))
	delete(s.progress, p.ID)
}

func (s *resourceSteps) summarize(summary *apitype.SummaryEvent) {
	operation := trace.SpanFromContext(s.ctx)
	for _, op := range slices.Sorted(maps.Keys(summary.ResourceChanges)) {
		operation.SetAttributes(attribute.Int("pulumi.summary."+string(op), summary.ResourceChanges[op]))
	}
	operation.SetAttributes(attribute.Bool("pulumi.summary.maybe_corrupt", summary.MaybeCorrupt))
}

func (s *resourceSteps) close(now time.Time) {
	trace.SpanFromContext(s.ctx).SetAttributes(attribute.Int(progress.AttrKeyResourceCount.Name, s.finished))
	for _, step := range s.open {
		step.span.SetStatus(codes.Error, errUnfinishedStep)
		step.span.End(trace.WithTimestamp(now))
	}
	for _, span := range s.progress {
		span.End(trace.WithTimestamp(now))
	}
	clear(s.open)
	clear(s.progress)
}

func stepName(op apitype.OpType) string {
	if action, changed := performedActions[op]; changed {
		return actionName(action)
	}
	if op == apitype.OpSame {
		return actionName(provider.ActionKeep)
	}
	return string(op) + " resource"
}

var progressNames = map[apitype.ProgressType]string{
	apitype.PluginDownload: "download plugin",
	apitype.PluginInstall:  "install plugin",
}

func progressName(kind apitype.ProgressType) string {
	if name, ok := progressNames[kind]; ok {
		return name
	}
	return string(kind)
}

func stepAttributes(m apitype.StepEventMetadata) []attribute.KeyValue {
	attrs := []attribute.KeyValue{
		attribute.String("pulumi.resource.urn", capIdentifier(m.URN)),
		attribute.String("pulumi.resource.type", capIdentifier(m.Type)),
		attribute.String("pulumi.step.op", string(m.Op)),
	}
	if m.Provider != "" {
		attrs = append(attrs, attribute.String("pulumi.resource.provider", m.Provider))
	}
	if parent := parentURN(m); parent != "" {
		attrs = append(attrs, attribute.String("pulumi.resource.parent", parent))
	}
	if m.Logical {
		attrs = append(attrs, attribute.Bool("pulumi.step.logical", true))
	}
	if len(m.Keys) > 0 {
		attrs = append(attrs, attribute.StringSlice("pulumi.step.replace_keys", m.Keys))
	}
	if len(m.Diffs) > 0 {
		attrs = append(attrs, attribute.StringSlice("pulumi.step.diffs", m.Diffs))
	}
	if len(m.DetailedDiff) > 0 {
		detailed := make([]string, 0, len(m.DetailedDiff))
		for _, path := range slices.Sorted(maps.Keys(m.DetailedDiff)) {
			detailed = append(detailed, path+": "+string(m.DetailedDiff[path].Kind))
		}
		attrs = append(attrs, attribute.StringSlice("pulumi.step.detailed_diff", detailed))
	}
	return attrs
}

func parentURN(m apitype.StepEventMetadata) string {
	switch {
	case m.New != nil && m.New.Parent != "":
		return m.New.Parent
	case m.Old != nil:
		return m.Old.Parent
	default:
		return ""
	}
}

func stepResourceID(m apitype.StepEventMetadata) string {
	switch {
	case m.New != nil && m.New.ID != "":
		return m.New.ID
	case m.Old != nil:
		return m.Old.ID
	default:
		return ""
	}
}

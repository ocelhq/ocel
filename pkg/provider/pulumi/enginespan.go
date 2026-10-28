package pulumi

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/ocelhq/ocel/pkg/tracing"
)

const instrumentation = "github.com/ocelhq/ocel/pkg/provider/pulumi"

var tracer = otel.Tracer(instrumentation)

func startEngineSpan(ctx context.Context, name string, setup WorkspaceSpec) (context.Context, trace.Span) {
	return tracer.Start(ctx, name, trace.WithAttributes(engineSpanAttributes(setup)...))
}

func engineSpanAttributes(setup WorkspaceSpec) []attribute.KeyValue {
	attrs := []attribute.KeyValue{
		attribute.String("pulumi.stack", setup.Stack),
		attribute.String("pulumi.project", string(setup.Project.Name)),
		attribute.Int("pulumi.parallel", setup.Parallel),
		attribute.Bool("pulumi.refresh", setup.Refresh),
	}
	if setup.Project.Backend != nil {
		attrs = append(attrs, attribute.String("pulumi.backend.url", maskSecrets(setup.Project.Backend.URL, setup.Secrets)))
	}
	return attrs
}

func endEngineSpan(span trace.Span, err error, secrets []string) {
	if err != nil {
		err = maskedMessage(maskSecrets(err.Error(), secrets))
	}
	tracing.EndSpan(span, err)
}

type maskedMessage string

func (m maskedMessage) Error() string { return string(m) }

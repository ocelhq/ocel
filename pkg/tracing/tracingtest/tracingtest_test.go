package tracingtest_test

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"

	"github.com/ocelhq/ocel/pkg/tracing/tracingtest"
)

func TestEachTestSeesOnlyTheSpansStartedUnderItsOwnContext(t *testing.T) {
	for _, name := range []string{"first", "second"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx, spans := tracingtest.Record(t)
			_, outside := otel.Tracer("test").Start(context.Background(), "outside")
			outside.End()
			parent, span := otel.Tracer("test").Start(ctx, name)
			_, child := otel.Tracer("test").Start(parent, name+" child")
			child.End()
			span.End()

			ended := spans.Ended()
			if len(ended) != 2 {
				t.Fatalf("saw %d spans, want this test's 2", len(ended))
			}
			own := tracingtest.SpanNamed(t, ended, name)
			if children := tracingtest.ChildrenOf(ended, own); len(children) != 1 || children[0].Name() != name+" child" {
				t.Errorf("children of %q = %v, want its one child", name, children)
			}
		})
	}
}

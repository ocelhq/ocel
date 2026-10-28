package runtrace

import (
	"slices"
	"strconv"

	"go.opentelemetry.io/otel/attribute"

	"github.com/ocelhq/ocel/pkg/progress"
)

func Attribute(key progress.AttrKey, value string) attribute.KeyValue {
	name := attribute.Key(key.Name)
	if key.Numeric {
		if n, err := strconv.ParseInt(value, 10, 64); err == nil {
			return name.Int64(n)
		}
	}
	return name.String(value)
}

const droppedSpanCountKey attribute.Key = "ocel.dropped_span_count"

var traceOnlyKeys = []attribute.Key{
	"ocel.app_count",
	"ocel.import_count",
	"ocel.registration_count",
	"ocel.worker",
	"ocel.framework",
	droppedSpanCountKey,
}

func isAllowed(key attribute.Key) bool {
	return slices.Contains(traceOnlyKeys, key) ||
		slices.ContainsFunc(progress.AttrKeys, func(allowed progress.AttrKey) bool { return allowed.Name == string(key) })
}

func filterAttributes(attrs []attribute.KeyValue) []attribute.KeyValue {
	if len(attrs) == 0 {
		return nil
	}
	out := make([]attribute.KeyValue, 0, len(attrs))
	for _, a := range attrs {
		if !a.Valid() {
			continue
		}
		if isAllowed(a.Key) {
			out = append(out, a)
		}
	}
	return out
}

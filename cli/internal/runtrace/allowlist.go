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

func isAllowed(key attribute.Key) bool {
	return slices.ContainsFunc(progress.AttrKeys, func(allowed progress.AttrKey) bool { return allowed.Name == string(key) })
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

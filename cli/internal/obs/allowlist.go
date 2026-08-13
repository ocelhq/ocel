package obs

import "go.opentelemetry.io/otel/attribute"

// The attribute vocabulary. This is the fixed set of keys a log record or a
// span is permitted to carry — anything else is dropped before it reaches
// either artifact. Every package that emits against the obs seam attaches
// attributes through these keys, never ad hoc strings, so a variable value
// or a credential can never ride along as an attribute: the key it would
// need doesn't exist.
const (
	AttrCommand       = attribute.Key("ocel.command")
	AttrStage         = attribute.Key("ocel.stage")
	AttrApp           = attribute.Key("ocel.app")
	AttrPhase         = attribute.Key("ocel.phase")
	AttrProvider      = attribute.Key("ocel.provider")
	AttrExitCode      = attribute.Key("ocel.exit_code")
	AttrErrorKind     = attribute.Key("ocel.error_kind")
	AttrResourceCount = attribute.Key("ocel.resource_count")
	AttrBytes         = attribute.Key("ocel.bytes")
	AttrRetryCount    = attribute.Key("ocel.retry_count")
	AttrDurationMS    = attribute.Key("ocel.duration_ms")
)

var allowedAttributes = map[attribute.Key]struct{}{
	AttrCommand:       {},
	AttrStage:         {},
	AttrApp:           {},
	AttrPhase:         {},
	AttrProvider:      {},
	AttrExitCode:      {},
	AttrErrorKind:     {},
	AttrResourceCount: {},
	AttrBytes:         {},
	AttrRetryCount:    {},
	AttrDurationMS:    {},
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
		if _, ok := allowedAttributes[a.Key]; ok {
			out = append(out, a)
		}
	}
	return out
}

package progress

import progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"

type AttrKey struct {
	Wire    progressv1.AttributeKey
	Name    string
	Numeric bool
}

type Attr struct {
	Key   AttrKey
	Value string
}

var (
	AttrKeyCommand        = AttrKey{Wire: progressv1.AttributeKey_ATTRIBUTE_KEY_COMMAND, Name: "ocel.command"}
	AttrKeySpanName       = AttrKey{Wire: progressv1.AttributeKey_ATTRIBUTE_KEY_SPAN_NAME, Name: "ocel.span_name"}
	AttrKeyApp            = AttrKey{Wire: progressv1.AttributeKey_ATTRIBUTE_KEY_APP, Name: "ocel.app"}
	AttrKeyPhase          = AttrKey{Wire: progressv1.AttributeKey_ATTRIBUTE_KEY_PHASE, Name: "ocel.phase"}
	AttrKeyProvider       = AttrKey{Wire: progressv1.AttributeKey_ATTRIBUTE_KEY_PROVIDER, Name: "ocel.provider"}
	AttrKeyExitCode       = AttrKey{Wire: progressv1.AttributeKey_ATTRIBUTE_KEY_EXIT_CODE, Name: "ocel.exit_code", Numeric: true}
	AttrKeyErrorKind      = AttrKey{Wire: progressv1.AttributeKey_ATTRIBUTE_KEY_ERROR_KIND, Name: "ocel.error_kind"}
	AttrKeyResourceCount  = AttrKey{Wire: progressv1.AttributeKey_ATTRIBUTE_KEY_RESOURCE_COUNT, Name: "ocel.resource_count", Numeric: true}
	AttrKeyBytes          = AttrKey{Wire: progressv1.AttributeKey_ATTRIBUTE_KEY_BYTES, Name: "ocel.bytes", Numeric: true}
	AttrKeyRetryCount     = AttrKey{Wire: progressv1.AttributeKey_ATTRIBUTE_KEY_RETRY_COUNT, Name: "ocel.retry_count", Numeric: true}
	AttrKeyDurationMS     = AttrKey{Wire: progressv1.AttributeKey_ATTRIBUTE_KEY_DURATION_MS, Name: "ocel.duration_ms", Numeric: true}
	AttrKeyResourceType   = AttrKey{Wire: progressv1.AttributeKey_ATTRIBUTE_KEY_RESOURCE_TYPE, Name: "ocel.resource_type"}
	AttrKeyResourceName   = AttrKey{Wire: progressv1.AttributeKey_ATTRIBUTE_KEY_RESOURCE_NAME, Name: "ocel.resource_name"}
	AttrKeyCached         = AttrKey{Wire: progressv1.AttributeKey_ATTRIBUTE_KEY_CACHED, Name: "ocel.cached"}
	AttrKeyResourceAction = AttrKey{Wire: progressv1.AttributeKey_ATTRIBUTE_KEY_RESOURCE_ACTION, Name: "ocel.resource_action"}
)

var AttrKeys = []AttrKey{
	AttrKeyCommand,
	AttrKeySpanName,
	AttrKeyApp,
	AttrKeyPhase,
	AttrKeyProvider,
	AttrKeyExitCode,
	AttrKeyErrorKind,
	AttrKeyResourceCount,
	AttrKeyBytes,
	AttrKeyRetryCount,
	AttrKeyDurationMS,
	AttrKeyResourceType,
	AttrKeyResourceName,
	AttrKeyCached,
	AttrKeyResourceAction,
}

func FindAttrKey(wire progressv1.AttributeKey) (AttrKey, bool) {
	for _, key := range AttrKeys {
		if key.Wire == wire {
			return key, true
		}
	}
	return AttrKey{}, false
}

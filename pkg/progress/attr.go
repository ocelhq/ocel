package progress

type AttrKey struct {
	Name    string
	Numeric bool
}

type Attr struct {
	Key   AttrKey
	Value string
}

var (
	AttrKeyCommand        = AttrKey{Name: "ocel.command"}
	AttrKeySpanName       = AttrKey{Name: "ocel.span_name"}
	AttrKeyApp            = AttrKey{Name: "ocel.app"}
	AttrKeyPhase          = AttrKey{Name: "ocel.phase"}
	AttrKeyProvider       = AttrKey{Name: "ocel.provider"}
	AttrKeyExitCode       = AttrKey{Name: "ocel.exit_code", Numeric: true}
	AttrKeyErrorKind      = AttrKey{Name: "ocel.error_kind"}
	AttrKeyResourceCount  = AttrKey{Name: "ocel.resource_count", Numeric: true}
	AttrKeyBytes          = AttrKey{Name: "ocel.bytes", Numeric: true}
	AttrKeyRetryCount     = AttrKey{Name: "ocel.retry_count", Numeric: true}
	AttrKeyDurationMS     = AttrKey{Name: "ocel.duration_ms", Numeric: true}
	AttrKeyResourceType   = AttrKey{Name: "ocel.resource_type"}
	AttrKeyResourceName   = AttrKey{Name: "ocel.resource_name"}
	AttrKeyCached         = AttrKey{Name: "ocel.cached"}
	AttrKeyResourceAction = AttrKey{Name: "ocel.resource_action"}
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

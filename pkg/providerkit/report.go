package providerkit

import (
	"fmt"
	"time"
)

// Reporter is how a port narrates a long operation. The kit owns stages, the
// event stream and the tracer; a port gets these three verbs and never sees an
// event, a stage id or a stream.
type Reporter interface {
	// Say is a headline the user reads while waiting.
	Say(message string)

	// Detail is a log line kept for the verbose stream and the trace.
	Detail(message string)

	// Span records a timed piece of work under the stage the kit is running the
	// port in. An engine's per-resource operations land here — the slow ones and
	// the failed ones — so the trace shows where a deploy's minutes went without
	// the kit knowing what an engine event is.
	Span(name string, start, end time.Time, err error, attrs ...Attr)
}

// Attr is one key and value on a span. Keys are the kit's; a port uses the ones
// it is given and invents none.
type Attr struct {
	Key   string
	Value string
}

const (
	AttrResourceType  = "resource.type"
	AttrResourceName  = "resource.name"
	AttrResourceCount = "resource.count"
)

// Code is the small set of outcomes the kit can turn into a wire status. A port
// returning anything other than a Refusal has failed, and the kit reports it as
// an internal failure with the vendor's message attached.
type Code string

const (
	// CodeInvalid: the request cannot be satisfied as written. The user edits
	// their config or their command and retries.
	CodeInvalid Code = "invalid"

	// CodeNotReady: the request is well-formed but the account is not in a state
	// to serve it — no bootstrap, a stale one, a missing feature. The message
	// names the command that fixes it.
	CodeNotReady Code = "not-ready"

	// CodeDenied: the credentials in play cannot do this. The kit pairs it with
	// the credential policy so the user can widen them.
	CodeDenied Code = "denied"

	// CodeOccupied: the thing exists and something still depends on it. This is
	// what stops a removal before it destroys anything.
	CodeOccupied Code = "occupied"

	// CodeBusy: another run is operating on this right now, or a killed one left
	// its lock behind. Nothing is wrong with the request; the message says how to
	// tell the two apart and how to release a stale lock. Distinct from Occupied
	// because the user's move is to wait or cancel, not to remove a dependent.
	CodeBusy Code = "busy"
)

// Refusal is a deliberate no, distinguishable from a failure. The kit maps it to
// a wire code and renders the message verbatim, so the message is addressed to
// the user and says what to do next.
type Refusal struct {
	Code    Code
	Message string
}

func (r Refusal) Error() string { return r.Message }

func Refuse(code Code, format string, args ...any) error {
	return Refusal{Code: code, Message: fmt.Sprintf(format, args...)}
}

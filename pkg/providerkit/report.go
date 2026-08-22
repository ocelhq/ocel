package providerkit

import (
	"time"

	"github.com/ocelhq/ocel/pkg/providerkit/ports"
)

type Reporter interface {
	Say(message string)

	Detail(message string)

	Span(name string, start, end time.Time, err error, attrs ...Attr)
}

type Code = ports.Code

const (
	CodeInvalid  = ports.CodeInvalid
	CodeNotReady = ports.CodeNotReady
	CodeDenied   = ports.CodeDenied
	CodeBusy     = ports.CodeBusy
)

type Refusal = ports.Refusal

var Refuse = ports.Refuse

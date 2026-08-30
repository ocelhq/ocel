package providerkit

import (
	"github.com/ocelhq/ocel/pkg/providerkit/ports"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type Reporter = edge.Reporter

type Attr = edge.Attr

type Code = ports.Code

const (
	CodeInvalid  = ports.CodeInvalid
	CodeNotReady = ports.CodeNotReady
	CodeDenied   = ports.CodeDenied
	CodeBusy     = ports.CodeBusy
)

type Refusal = ports.Refusal

var Refuse = ports.Refuse

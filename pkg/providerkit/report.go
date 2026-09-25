package providerkit

import (
	"github.com/ocelhq/ocel/pkg/providerkit/ports"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type Progress = edge.Progress

type Attr = edge.Attr

type Code = ports.Code

const (
	CodeInvalid       = ports.CodeInvalid
	CodeNotReady      = ports.CodeNotReady
	CodeDenied        = ports.CodeDenied
	CodeBusy          = ports.CodeBusy
	CodeUnknownOption = ports.CodeUnknownOption
)

type Refusal = ports.Refusal

var Refuse = ports.Refuse

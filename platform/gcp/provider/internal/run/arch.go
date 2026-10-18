package run

import (
	"github.com/ocelhq/ocel/pkg/arch"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func RefuseNotX8664(architecture, what string) error {
	if arch.Architecture(architecture) == arch.X8664 {
		return nil
	}
	return refusal.Refuse(refusal.CodeInvalid,
		"%s is built for %s, and Cloud Run runs %s alone: build it for %s, or run it somewhere that offers %s",
		what, arch.Architecture(architecture), arch.X8664, arch.X8664, arch.Architecture(architecture))
}

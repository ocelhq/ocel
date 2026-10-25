package functioncode

import (
	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func RefuseContainerFramework(framework buildoutput.Framework) error {
	if framework.Name == buildoutput.FrameworkNext {
		return nil
	}
	return refusal.Refuse(refusal.CodeInvalid, "a %s container has no Dockerfile: add one", framework.Name)
}

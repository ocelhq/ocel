package deploy

import (
	"github.com/ocelhq/ocel/cli/internal/runui"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

type deployOutcome struct {
	bindings    []*bindingsv1.Binding
	functions   []*progressv1.FunctionOutput
	apps        []*progressv1.AppResult
	urlNote     string
	promotionID string
	flip        runui.Flip
}

func (o *deployOutcome) collect(ui *runui.Session) func(*progressv1.OperationEvent) {
	return func(ev *progressv1.OperationEvent) {
		ui.Event(ev)
		res := ev.GetResult()
		if res == nil {
			return
		}
		o.bindings = res.GetBindings()
		o.functions = res.GetFunctions()
		o.apps = res.GetApps()
		o.urlNote = res.GetUrlNote()
		o.promotionID = res.GetPromotionId()
		o.flip = runui.FlipFor(res.GetFlipBound())
	}
}

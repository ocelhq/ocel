package deploy

import (
	"github.com/ocelhq/ocel/cli/internal/runui"
	linksv1 "github.com/ocelhq/ocel/pkg/proto/common/links/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

type deployOutcome struct {
	links       []*linksv1.Link
	functions   []*progressv1.FunctionOutput
	appURLs     []string
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
		o.links = res.GetLinks()
		o.functions = res.GetFunctions()
		o.appURLs = res.GetAppUrls()
		o.urlNote = res.GetUrlNote()
		o.promotionID = res.GetPromotionId()
		o.flip = runui.FlipFor(res.GetFlipBound())
	}
}

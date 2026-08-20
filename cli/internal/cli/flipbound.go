package cli

import (
	"fmt"
	"strconv"

	"github.com/ocelhq/ocel/cli/internal/deployui"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/progress/v1"
)

func flipFor(bound *progressv1.FlipBound) deployui.Flip {
	return deployui.Flip{Note: flipNote(bound), Bound: bound}
}

func flipNote(bound *progressv1.FlipBound) string {
	typical := bound.GetTypicalMs()
	if typical <= 0 {
		return ""
	}
	if bound.GetPublished() {
		return fmt.Sprintf("propagates within ~%s", flipDuration(typical))
	}
	return fmt.Sprintf("propagates in ~%s (typical, not guaranteed)", flipDuration(typical))
}

func flipDuration(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%d ms", ms)
	}
	return strconv.FormatFloat(float64(ms)/1000, 'f', -1, 64) + " s"
}

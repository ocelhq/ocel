package runui

import (
	"fmt"
	"strconv"
	"time"

	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

func FlipFor(bound *progressv1.FlipBound) Flip {
	return Flip{Note: FlipNote(bound), Bound: bound}
}

func FlipNote(bound *progressv1.FlipBound) string {
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

func EpochDate(sec int64) string {
	if sec == 0 {
		return "—"
	}
	return time.Unix(sec, 0).UTC().Format("2006-01-02")
}

func EpochDateTime(sec int64) string {
	if sec == 0 {
		return "—"
	}
	return time.Unix(sec, 0).UTC().Format("2006-01-02 15:04:05 UTC")
}

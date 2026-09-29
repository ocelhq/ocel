package terminal

import (
	"fmt"
	"strconv"

	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

func PropagationNote(bound *progressv1.Propagation) string {
	typical := bound.GetTypicalMs()
	if typical <= 0 {
		return ""
	}
	if bound.GetPublished() {
		return fmt.Sprintf("propagates within ~%s", propagationDuration(typical))
	}
	return fmt.Sprintf("propagates in ~%s (typical, not guaranteed)", propagationDuration(typical))
}

func propagationDuration(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%d ms", ms)
	}
	return strconv.FormatFloat(float64(ms)/1000, 'f', -1, 64) + " s"
}

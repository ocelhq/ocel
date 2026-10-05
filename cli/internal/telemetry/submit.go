package telemetry

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/ocelhq/ocel/cli/internal/userconfig"
)

const DebugPrefix = "[telemetry] "

func Submit(w io.Writer, resolution Resolution, event Event) {
	if !resolution.Enabled {
		return
	}
	if !resolution.Debug {
		if !isBannerShown(userconfig.Read()) {
			return
		}
		if spool, err := OpenSpool(); err == nil {
			_ = spool.Append(event)
		}
		return
	}
	line, err := json.Marshal(event)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "%s%s\n", DebugPrefix, line)
}

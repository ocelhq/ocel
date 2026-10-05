package telemetry

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/ocelhq/ocel/cli/internal/userconfig"
)

const DebugPrefix = "[telemetry] "

func Submit(w io.Writer, resolution Resolution, event Event) bool {
	if !resolution.Enabled {
		return false
	}
	if !resolution.Debug {
		if !isBannerShown(userconfig.Read()) {
			return false
		}
		spool, err := OpenSpool()
		return err == nil && spool.Append(event) == nil
	}
	line, err := json.Marshal(event)
	if err != nil {
		return false
	}
	_, err = fmt.Fprintf(w, "%s%s\n", DebugPrefix, line)
	return err == nil
}

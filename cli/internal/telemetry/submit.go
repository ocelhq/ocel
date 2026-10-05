package telemetry

import (
	"encoding/json"
	"fmt"
	"io"
)

const DebugPrefix = "[telemetry] "

func Submit(w io.Writer, resolution Resolution, event Event) {
	if !resolution.Debug {
		return
	}
	line, err := json.Marshal(event)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "%s%s\n", DebugPrefix, line)
}

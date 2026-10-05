package telemetry

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/ocelhq/ocel/cli/internal/docsurl"
	"github.com/ocelhq/ocel/cli/internal/userconfig"
)

const bannerShownKey = "telemetry_banner_shown"

func AnnounceOnce(w io.Writer, res Resolution) {
	if !res.Enabled || res.Debug || bannerShown(userconfig.Read()) {
		return
	}
	fmt.Fprintf(w, "Ocel collects anonymous usage data: which commands run, and whether they succeed. Never your code, paths or names.\nLearn more: %s\nTurn it off: %s=0\n\n", docsurl.FormatTelemetryPage(), EnvVar)
	_ = userconfig.Update(func(s userconfig.Settings) {
		s[bannerShownKey], _ = json.Marshal(true)
	})
}

func bannerShown(s userconfig.Settings) bool {
	var shown bool
	return json.Unmarshal(s[bannerShownKey], &shown) == nil && shown
}

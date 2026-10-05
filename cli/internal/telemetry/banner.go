package telemetry

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/ocelhq/ocel/cli/internal/docsurl"
	"github.com/ocelhq/ocel/cli/internal/userconfig"
)

const bannerShownKey = "telemetry_banner_shown"

func PrintBannerOnce(w io.Writer, resolution Resolution) {
	if !resolution.IsCollecting() || isBannerShown(userconfig.Read()) {
		return
	}
	shownElsewhere := false
	_ = userconfig.Update(func(s userconfig.Settings) {
		if isBannerShown(s) {
			shownElsewhere = true
			return
		}
		s[bannerShownKey], _ = json.Marshal(true)
	})
	if shownElsewhere {
		return
	}
	fmt.Fprintf(w, "Ocel collects anonymous usage data: which commands run, and whether they succeed. Never your code, paths or names.\nLearn more: %s\nTurn it off: %s=0\n\n", docsurl.FormatTelemetryPage(), EnvVar)
}

func isBannerShown(s userconfig.Settings) bool {
	var shown bool
	return json.Unmarshal(s[bannerShownKey], &shown) == nil && shown
}

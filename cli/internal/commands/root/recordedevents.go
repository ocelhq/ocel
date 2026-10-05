package root

import (
	"time"

	"github.com/ocelhq/ocel/cli/internal/telemetry"
	"github.com/ocelhq/ocel/cli/internal/userconfig"
)

func (c *command) recordEvent(payload telemetry.Payload) {
	resolution := telemetry.Resolve(telemetry.WriteKey, telemetry.Endpoint)
	if !resolution.Enabled {
		return
	}
	installID, idErr := userconfig.EnsureInstallID()
	if idErr != nil {
		return
	}
	event, buildErr := telemetry.NewEvent(telemetry.NewIdentity(installID), time.Now(), payload)
	if buildErr != nil {
		return
	}
	telemetry.Submit(c.root.ErrOrStderr(), resolution, event)
}

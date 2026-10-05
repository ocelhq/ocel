package root

import (
	"time"

	"github.com/ocelhq/ocel/cli/internal/telemetry"
	"github.com/ocelhq/ocel/cli/internal/userconfig"
)

func (c *command) recordEvent(payload telemetry.Payload) bool {
	resolution := telemetry.Resolve(telemetry.WriteKey, telemetry.Endpoint)
	if !resolution.Enabled {
		return false
	}
	installID, idErr := userconfig.EnsureInstallID()
	if idErr != nil {
		return false
	}
	event, buildErr := telemetry.NewEvent(telemetry.NewIdentity(installID), time.Now(), payload)
	if buildErr != nil {
		return false
	}
	return telemetry.Submit(c.root.ErrOrStderr(), resolution, event)
}

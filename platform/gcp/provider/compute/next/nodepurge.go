package next

import (
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/processenv"
	"github.com/ocelhq/ocel/pkg/provider"
)

func NewNodePurgeEnv(spec provider.StackSpec, purgeSecret string) map[string]string {
	isr := spec.App.ISR
	if isr == nil || Serves(spec.App) {
		return nil
	}
	signs := purgeSecret != "" && isr.PurgeURL != ""
	if !signs && !purgesCloudCDN(spec) {
		return nil
	}
	env := map[string]string{processenv.StorageCoordinateEnvVar: spec.App.StorageCoordinate.StorageCoordinate()}
	if signs {
		env[purgeSecretEnvVar] = purgeSecret
		env[edge.PurgeURLVar] = isr.PurgeURL
	}
	return env
}

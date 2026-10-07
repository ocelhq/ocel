package build

import (
	"maps"
	"slices"

	"github.com/ocelhq/ocel/pkg/processenv"
)

func environmentOf(values AppVariables, liveDir string) (env map[string]string, unset []string) {
	env = make(map[string]string, len(values.Env)+1)
	maps.Copy(env, values.Env)
	env[processenv.LiveDirEnvVar] = liveDir
	for key := range values.Env {
		unset = append(unset, processenv.DeliveredVariablePrefix+key)
	}
	for key := range values.Live {
		unset = append(unset, key, processenv.DeliveredVariablePrefix+key)
	}
	slices.Sort(unset)
	return env, slices.Compact(unset)
}

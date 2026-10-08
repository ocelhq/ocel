package build

import (
	"maps"
	"slices"

	"github.com/ocelhq/ocel/pkg/processenv"
)

type environment struct {
	set   map[string]string
	unset []string
}

func composeEnvironment(values AppVariables, liveDir string) environment {
	env := make(map[string]string, len(values.Env)+1)
	maps.Copy(env, values.Env)
	maps.Copy(env, values.BindingProxyEnv)
	env[processenv.LiveDirEnvVar] = liveDir
	var unset []string
	for key := range values.Env {
		unset = append(unset, processenv.DeliveredVariablePrefix+key)
	}
	for key := range values.Live {
		unset = append(unset, key, processenv.DeliveredVariablePrefix+key)
	}
	slices.Sort(unset)
	return environment{set: env, unset: slices.Compact(unset)}
}

package build

import (
	"maps"

	"github.com/ocelhq/ocel/pkg/processenv"
)

func withLiveDir(env map[string]string, dir string) map[string]string {
	out := make(map[string]string, len(env)+1)
	maps.Copy(out, env)
	out[processenv.LiveDirEnvVar] = dir
	return out
}

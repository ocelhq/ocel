package containerimage

import (
	"path"
	"strings"

	"github.com/ocelhq/ocel/pkg/buildoutput"
)

const NextServerPreloadFile = "server-preload.mjs"

var NextServerPreloadPath = path.Join(FrameworkRuntimeDir(buildoutput.FrameworkNext), NextServerPreloadFile)

func AppendNextServerPreload(present func(file string) bool, env []string) []string {
	if !present(NextServerPreloadPath) {
		return env
	}
	preload := "--import " + NextServerPreloadPath
	out := make([]string, 0, len(env)+1)
	appended := false
	for _, entry := range env {
		name, value, _ := strings.Cut(entry, "=")
		if name != NodeOptionsEnvVar {
			out = append(out, entry)
			continue
		}
		out = append(out, NodeOptionsEnvVar+"="+strings.TrimSpace(value+" "+preload))
		appended = true
	}
	if !appended {
		out = append(out, NodeOptionsEnvVar+"="+preload)
	}
	return out
}

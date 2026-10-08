package containerimage

import (
	"path"
	"strings"

	"github.com/ocelhq/ocel/pkg/buildoutput"
)

const (
	NextServerPreloadFile = "server-preload.mjs"
	nodeOptionsVar        = "NODE_OPTIONS"
)

var NextServerPreloadPath = path.Join(RuntimeRoot, buildoutput.FrameworkNext, NextServerPreloadFile)

func AppendNextServerPreload(present func(file string) bool, env []string) []string {
	if !present(NextServerPreloadPath) {
		return env
	}
	preload := "--import " + NextServerPreloadPath
	out := make([]string, 0, len(env)+1)
	appended := false
	for _, entry := range env {
		name, value, _ := strings.Cut(entry, "=")
		if name != nodeOptionsVar {
			out = append(out, entry)
			continue
		}
		out = append(out, nodeOptionsVar+"="+strings.TrimSpace(value+" "+preload))
		appended = true
	}
	if !appended {
		out = append(out, nodeOptionsVar+"="+preload)
	}
	return out
}

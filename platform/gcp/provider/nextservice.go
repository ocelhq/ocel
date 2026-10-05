package gcp

import (
	"strconv"
	"time"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/provider"
)

const (
	nextMemoryMB       = 2048
	nextRequestTimeout = time.Minute
)

const memoryEnvVar = "OCEL_FUNCTION_MEMORY_MB"

func servesNext(app *provider.AppSpec) bool {
	return app.Framework == buildoutput.FrameworkNext
}

func nextServing(s serving) serving {
	if s.memory == 0 {
		s.memory = nextMemoryMB
	}
	if s.timeout == 0 {
		s.timeout = nextRequestTimeout
	}
	return s
}

func nextEnv(s serving) map[string]string {
	return map[string]string{memoryEnvVar: strconv.Itoa(s.memory)}
}

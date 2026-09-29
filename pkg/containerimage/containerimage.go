package containerimage

import (
	"regexp"
	"strconv"
)

const PinnedPattern = `^([^/@:[:space:]]+(:[0-9]+)?/)?[^/@:[:space:]]+(/[^/@:[:space:]]+)*@sha256:[0-9a-f]{64}$`

const HealthCheckPathPattern = `^/[^#?[:space:][:cntrl:]]*$`

var (
	pinned          = regexp.MustCompile(PinnedPattern)
	healthCheckPath = regexp.MustCompile(HealthCheckPathPattern)
)

func IsPinned(imageRef string) bool {
	return pinned.MatchString(imageRef)
}

func IsHealthCheckPath(path string) bool {
	return healthCheckPath.MatchString(path)
}

const (
	PortEnvVar = "PORT"
	Port       = 8080
)

var PortText = strconv.Itoa(Port)

const (
	RuntimePath = "/ocel/bin/runtime"
	LivePath    = "/ocel/live"
)

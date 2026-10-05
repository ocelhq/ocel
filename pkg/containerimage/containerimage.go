package containerimage

import (
	"regexp"
	"strconv"
)

const PinnedPattern = `^([^/@: \t\n\v\f\r]+(:[0-9]+)?/)?[^/@: \t\n\v\f\r]+(/[^/@: \t\n\v\f\r]+)*@sha256:[0-9a-f]{64}$`

const HealthCheckPathPattern = `^/[^#? \t\n\v\f\r\x00-\x1f\x7f]*$`

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

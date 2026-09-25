package providerkit

import "regexp"

const PinnedImagePattern = `^([^/@:[:space:]]+(:[0-9]+)?/)?[^/@:[:space:]]+(/[^/@:[:space:]]+)*@sha256:[0-9a-f]{64}$`

const HealthCheckPathPattern = `^/[^#?[:space:][:cntrl:]]*$`

var (
	pinnedImage     = regexp.MustCompile(PinnedImagePattern)
	healthCheckPath = regexp.MustCompile(HealthCheckPathPattern)
)

func PinnedImage(imageRef string) bool {
	return pinnedImage.MatchString(imageRef)
}

func HealthCheckPath(path string) bool {
	return healthCheckPath.MatchString(path)
}

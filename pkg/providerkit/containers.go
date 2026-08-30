package providerkit

import "regexp"

const PinnedImagePattern = `^([^/@:[:space:]]+(:[0-9]+)?/)?[^/@:[:space:]]+(/[^/@:[:space:]]+)*@sha256:[0-9a-f]{64}$`

var pinnedImage = regexp.MustCompile(PinnedImagePattern)

func PinnedImage(ref string) bool {
	return pinnedImage.MatchString(ref)
}

package edge

import (
	"fmt"
	"regexp"
	"strings"
)

const (
	dnsLabelMax = 63
	dnsNameMax  = 253
)

var dnsLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

func isDNSName(name string) bool {
	named := strings.ToLower(name)
	if len(named) > dnsNameMax {
		return false
	}
	for _, label := range strings.Split(named, ".") {
		if len(label) > dnsLabelMax || !dnsLabel.MatchString(label) {
			return false
		}
	}
	return true
}

func RefuseUnusablePreviewBase(base string) error {
	dotted := strings.Contains(base, ".") && len(base)+len(LivenessProbeLabel)+1 <= dnsNameMax
	if !dotted || !isDNSName(base) {
		return fmt.Errorf("%q is not a usable preview base: want dotted dns labels of ≤%d bytes, with %s ≤%d bytes",
			base, dnsLabelMax, ProbeHostname(PreviewWildcard(base)), dnsNameMax)
	}
	return nil
}

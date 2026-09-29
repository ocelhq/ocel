package edge

import "strings"

const LivenessProbeLabel = "ocel-edge-probe"

const LivenessProbePath = "/.well-known/ocel-edge"

func ProbeHostname(hostname string) string {
	if rest, ok := strings.CutPrefix(hostname, "*."); ok {
		return LivenessProbeLabel + "." + rest
	}
	return hostname
}

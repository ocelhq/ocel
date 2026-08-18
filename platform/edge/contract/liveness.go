package edge

import "strings"

const HeaderEdge = "x-ocel-edge"

const LivenessProbeLabel = "ocel-edge-probe"

func ProbeHostname(hostname string) string {
	if rest, ok := strings.CutPrefix(hostname, "*."); ok {
		return LivenessProbeLabel + "." + rest
	}
	return hostname
}

func ServedBy(header string, kind Kind) bool {
	return strings.EqualFold(strings.TrimSpace(header), string(kind))
}

package certs

import (
	"strings"
)

type Certificates struct {
	ACM  ACM
	Pins map[string]string
}

func CertificatesFor(region string, deps Deps, pins map[string]string) Certificates {
	return Certificates{ACM: ACMFor(region, deps), Pins: NormalizePins(pins)}
}

func (c Certificates) Issues() bool { return c.ACM.API != nil }

func (c Certificates) PinFor(hostname string) string { return c.Pins[hostname] }

func (c Certificates) IgnoresPinFor(hostname string) bool {
	return !c.Issues() && c.PinFor(hostname) != ""
}

func (c Certificates) Wants(recorded Certificate, hostname string) string {
	if pinned := c.PinFor(hostname); pinned != "" {
		return pinned
	}
	return recorded.ARN
}

func (c Certificates) Unpinned(hostnames []string) []string {
	var wanted []string
	for _, hostname := range hostnames {
		if c.PinFor(hostname) == "" {
			wanted = append(wanted, hostname)
		}
	}
	return wanted
}

func NormalizePins(pins map[string]string) map[string]string {
	if len(pins) == 0 {
		return nil
	}
	out := make(map[string]string, len(pins))
	for host, arn := range pins {
		if arn = strings.TrimSpace(arn); arn != "" {
			out[strings.ToLower(strings.TrimSpace(host))] = arn
		}
	}
	return out
}

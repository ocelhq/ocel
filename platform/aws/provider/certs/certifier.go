package certs

import (
	"strings"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type Certifier struct {
	Issuer Issuer
	Pins   map[string]string
}

func CertifierFor(front edge.Edge, deps Deps, pins map[string]string) Certifier {
	return Certifier{Issuer: IssuerFor(front, deps), Pins: NormalizePins(pins)}
}

func (c Certifier) Issues() bool { return c.Issuer.API != nil }

func (c Certifier) PinFor(hostname string) string { return c.Pins[hostname] }

func (c Certifier) IgnoresPinFor(hostname string) bool {
	return !c.Issues() && c.PinFor(hostname) != ""
}

func (c Certifier) Wants(recorded Certificate, hostname string) string {
	if pinned := c.PinFor(hostname); pinned != "" {
		return pinned
	}
	return recorded.ARN
}

func (c Certifier) Unpinned(hostnames []string) []string {
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

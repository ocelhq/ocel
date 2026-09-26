package providerserver

import (
	"slices"
	"strings"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type ConfiguredHost struct {
	Hostname string
	App      string
}

func productionHosts(hosts []*contractv1.ConfiguredHostname) ([]ConfiguredHost, error) {
	var out []ConfiguredHost
	for _, raw := range hosts {
		host, err := productionHost(raw.GetHostname())
		if err != nil {
			return nil, err
		}
		if host == "" || slices.ContainsFunc(out, func(held ConfiguredHost) bool { return held.Hostname == host }) {
			continue
		}
		out = append(out, ConfiguredHost{Hostname: host, App: raw.GetApp()})
	}
	return out, nil
}

func hostnamesOf(hosts []ConfiguredHost) []string {
	named := make([]string, 0, len(hosts))
	for _, host := range hosts {
		named = append(named, host.Hostname)
	}
	return named
}

func productionHost(raw string) (string, error) {
	host := strings.TrimSuffix(strings.TrimSpace(strings.ToLower(raw)), ".")
	switch {
	case host == "":
		return "", nil
	case strings.ContainsAny(host, "/:*"), !strings.Contains(host, "."):
		return "", refusal.Refuse(refusal.CodeInvalid,
			"%q is not a production hostname: pass a name like app.acme.com — a wildcard belongs to domains.preview", raw)
	}
	return host, nil
}

func previewBaseDomain(raw string) (string, error) {
	base := strings.TrimSuffix(strings.TrimSpace(strings.ToLower(raw)), ".")
	switch {
	case base == "":
		return "", refusal.Refuse(refusal.CodeInvalid, "a domain is required, e.g. `ocel domain use --preview preview.acme.com`")
	case strings.HasPrefix(base, "*."):
		return "", refusal.Refuse(refusal.CodeInvalid,
			"give the domain itself, not the wildcard: every preview is served on its own subdomain of it, so pass %q",
			strings.TrimPrefix(base, "*."))
	case strings.ContainsAny(base, "/:*"), !strings.Contains(base, "."):
		return "", refusal.Refuse(refusal.CodeInvalid, "%q is not a domain name: pass a hostname like preview.acme.com", raw)
	}
	return base, nil
}

func provisionedList(hosts []string) string {
	if len(hosts) == 0 {
		return "no production hostname at all"
	}
	return strings.Join(hosts, ", ")
}

func recordLines(records []edge.Record) []string {
	var out []string
	for _, rec := range records {
		if line := rec.String(); !slices.Contains(out, line) {
			out = append(out, line)
		}
	}
	return out
}

func flipWindow(records edge.DNSRecords) string {
	if records == nil {
		return unknownTTL
	}
	if ttl := records.TTL(); ttl > 0 {
		return ttl.String()
	}
	return unknownTTL
}

const unknownTTL = "whatever TTL your DNS provider serves that record with"

package providerkit

import (
	"maps"
	"slices"
	"strings"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type EdgeStackState struct {
	Edge  edge.StackState    `json:"edge"`
	Hosts map[string]Settled `json:"hosts,omitempty"`
}

type Settled struct {
	// TODO(#390): the edge lands the certificate surface. Whatever fills this in must carry
	// whether the operator pinned it: a pinned certificate is never ocel's to delete.
	Certificate string        `json:"certificate,omitempty"`
	Written     []edge.Record `json:"written,omitempty"`
	Owed        []edge.Record `json:"owed,omitempty"`
	Probe       Probe         `json:"probe,omitzero"`
}

type Probe struct {
	At   int64     `json:"at,omitempty"`
	OK   bool      `json:"ok,omitempty"`
	Edge edge.Kind `json:"edge,omitempty"`
}

func (s Settled) Serving() edge.Kind {
	if !s.Probe.OK {
		return ""
	}
	return s.Probe.Edge
}

type Wildcard struct {
	BaseDomain string    `json:"base_domain,omitempty"`
	Edge       edge.Kind `json:"edge,omitempty"`
	Scope      string    `json:"scope,omitempty"`
	GrammarMin uint32    `json:"grammar_min,omitempty"`
	GrammarMax uint32    `json:"grammar_max,omitempty"`
	Settled    Settled   `json:"settled,omitzero"`
}

func (w Wildcard) Hostname() string { return edge.PreviewWildcard(w.BaseDomain) }

func (w Wildcard) Holder() (edge.Kind, bool) { return w.Edge, w.Edge != "" }

func (s EdgeStackState) Host(hostname string) Settled { return s.Hosts[hostname] }

func (s EdgeStackState) Hostnames() []string { return slices.Sorted(maps.Keys(s.Hosts)) }

func (s EdgeStackState) Ready(hostname string, kind edge.Kind) bool {
	return kind != "" && s.Host(hostname).Serving() == kind
}

func (s *EdgeStackState) Settle(hostname string, settled Settled) {
	if s.Hosts == nil {
		s.Hosts = map[string]Settled{}
	}
	s.Hosts[hostname] = settled
}

func (s *EdgeStackState) Forget(hostname string) {
	delete(s.Hosts, hostname)
	if len(s.Hosts) == 0 {
		s.Hosts = nil
	}
}

func productionHosts(hosts []string) ([]string, error) {
	var out []string
	for _, raw := range hosts {
		host, err := productionHost(raw)
		if err != nil {
			return nil, err
		}
		if host != "" && !slices.Contains(out, host) {
			out = append(out, host)
		}
	}
	return out, nil
}

func productionHost(raw string) (string, error) {
	host := strings.TrimSuffix(strings.TrimSpace(strings.ToLower(raw)), ".")
	switch {
	case host == "":
		return "", nil
	case strings.ContainsAny(host, "/:*"), !strings.Contains(host, "."):
		return "", Refuse(CodeInvalid,
			"%q is not a production hostname: pass a name like app.acme.com — a wildcard belongs to domains.preview", raw)
	}
	return host, nil
}

func previewBaseDomain(raw string) (string, error) {
	base := strings.TrimSuffix(strings.TrimSpace(strings.ToLower(raw)), ".")
	switch {
	case base == "":
		return "", Refuse(CodeInvalid, "a domain is required, e.g. `ocel domain use --preview preview.acme.com`")
	case strings.HasPrefix(base, "*."):
		return "", Refuse(CodeInvalid,
			"give the domain itself, not the wildcard: every preview is served on its own subdomain of it, so pass %q",
			strings.TrimPrefix(base, "*."))
	case strings.ContainsAny(base, "/:*"), !strings.Contains(base, "."):
		return "", Refuse(CodeInvalid, "%q is not a domain name: pass a hostname like preview.acme.com", raw)
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

func flipWindow(writer edge.DNSWriter) string {
	if ttl := edge.WriteTTL(writer); ttl > 0 {
		return ttl.String()
	}
	return "whatever TTL your DNS provider serves that record with"
}

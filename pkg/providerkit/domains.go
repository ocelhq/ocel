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
	Certificate Certificate   `json:"certificate,omitzero"`
	Superseded  []Certificate `json:"superseded,omitempty"`
	Written     []edge.Record `json:"written,omitempty"`
	Owed        []edge.Record `json:"owed,omitempty"`
	Probe       Probe         `json:"probe,omitzero"`
}

func (s *Settled) Supersede(cert Certificate) {
	if !cert.Held() || cert.ID == s.Certificate.ID || holds(s.Superseded, cert) {
		return
	}
	s.Superseded = append(s.Superseded, cert)
}

func (s Settled) certificates() []Certificate {
	held := make([]Certificate, 0, 1+len(s.Superseded))
	for _, cert := range append([]Certificate{s.Certificate}, s.Superseded...) {
		if cert.Held() && !holds(held, cert) {
			held = append(held, cert)
		}
	}
	return held
}

func holds(certificates []Certificate, cert Certificate) bool {
	return slices.ContainsFunc(certificates, func(other Certificate) bool { return other.ID == cert.ID })
}

func (s Settled) WrittenRecords() []edge.Record {
	written := slices.Clone(s.Written)
	for _, cert := range s.certificates() {
		written = mergeRecords(written, cert.Written)
	}
	return written
}

func (s Settled) OwedRecords() []edge.Record {
	owed := slices.Clone(s.Owed)
	for _, cert := range s.certificates() {
		owed = mergeRecords(owed, cert.Owed)
	}
	return owed
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

func (s EdgeStackState) WrittenRecords() []edge.Record {
	var written []edge.Record
	for _, hostname := range s.Hostnames() {
		written = mergeRecords(written, s.Hosts[hostname].WrittenRecords())
	}
	return written
}

func (s EdgeStackState) PointerRecords() []edge.Record {
	var written []edge.Record
	for _, hostname := range s.Hostnames() {
		written = mergeRecords(written, s.Hosts[hostname].Written)
	}
	return written
}

func (s EdgeStackState) OwedRecords() []edge.Record {
	var owed []edge.Record
	for _, hostname := range s.Hostnames() {
		owed = mergeRecords(owed, s.Hosts[hostname].OwedRecords())
	}
	return owed
}

func (s EdgeStackState) Certificates() []Certificate {
	var held []Certificate
	for _, hostname := range s.Hostnames() {
		for _, cert := range s.Hosts[hostname].certificates() {
			if !holds(held, cert) {
				held = append(held, cert)
			}
		}
	}
	return held
}

func (s EdgeStackState) Uses(id string) bool {
	if id == "" {
		return false
	}
	for _, settled := range s.Hosts {
		if settled.Certificate.ID == id {
			return true
		}
	}
	return false
}

func mergeRecords(into, records []edge.Record) []edge.Record {
	for _, rec := range records {
		if !slices.Contains(into, rec) {
			into = append(into, rec)
		}
	}
	return into
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

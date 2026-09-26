package stackrecords

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/records"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type EdgeState struct {
	Kind  edge.Kind                `json:"kind,omitempty"`
	Edge  edge.StackState          `json:"edge"`
	Hosts map[string]HostnameState `json:"hosts,omitempty"`
}

type HostnameState struct {
	Certificate provider.Certificate   `json:"certificate,omitzero"`
	Superseded  []provider.Certificate `json:"superseded,omitempty"`
	Written     []edge.Record          `json:"written,omitempty"`
	Manual      []edge.Record          `json:"owed,omitempty"`
	Probe       ServeProbe             `json:"probe,omitzero"`
}

func (s *HostnameState) Supersede(cert provider.Certificate) {
	if !cert.Issued() || cert.ID == s.Certificate.ID || holds(s.Superseded, cert) {
		return
	}
	s.Superseded = append(s.Superseded, cert)
}

func (s HostnameState) Certificates() []provider.Certificate {
	held := make([]provider.Certificate, 0, 1+len(s.Superseded))
	for _, cert := range append([]provider.Certificate{s.Certificate}, s.Superseded...) {
		if cert.Issued() && !holds(held, cert) {
			held = append(held, cert)
		}
	}
	return held
}

func holds(certificates []provider.Certificate, cert provider.Certificate) bool {
	return slices.ContainsFunc(certificates, func(other provider.Certificate) bool { return other.ID == cert.ID })
}

func (s HostnameState) WrittenRecords() []edge.Record {
	written := slices.Clone(s.Written)
	for _, cert := range s.Certificates() {
		written = mergeRecords(written, cert.Written)
	}
	return written
}

func (s HostnameState) ManualRecords() []edge.Record {
	manual := slices.Clone(s.Manual)
	for _, cert := range s.Certificates() {
		manual = mergeRecords(manual, cert.Manual)
	}
	return manual
}

type ServeProbe struct {
	At   int64     `json:"at,omitempty"`
	OK   bool      `json:"ok,omitempty"`
	Edge edge.Kind `json:"edge,omitempty"`
}

func (s HostnameState) Serving() edge.Kind {
	if !s.Probe.OK {
		return ""
	}
	return s.Probe.Edge
}

type Wildcard struct {
	BaseDomain string        `json:"base_domain,omitempty"`
	Edge       edge.Kind     `json:"edge,omitempty"`
	Scope      string        `json:"scope,omitempty"`
	GrammarMin uint32        `json:"grammar_min,omitempty"`
	GrammarMax uint32        `json:"grammar_max,omitempty"`
	Host       HostnameState `json:"settled,omitzero"`
}

func (w Wildcard) Hostname() string { return edge.PreviewWildcard(w.BaseDomain) }

func (w Wildcard) OwningEdge() (edge.Kind, bool) { return w.Edge, w.Edge != "" }

func (s EdgeState) Host(hostname string) HostnameState { return s.Hosts[hostname] }

func (s EdgeState) Hostnames() []string { return slices.Sorted(maps.Keys(s.Hosts)) }

func (s EdgeState) Ready(hostname string, kind edge.Kind) bool {
	return kind != "" && s.Host(hostname).Serving() == kind
}

func (s *EdgeState) SetHost(hostname string, state HostnameState) {
	if s.Hosts == nil {
		s.Hosts = map[string]HostnameState{}
	}
	s.Hosts[hostname] = state
}

func (s EdgeState) WrittenRecords() []edge.Record {
	var written []edge.Record
	for _, hostname := range s.Hostnames() {
		written = mergeRecords(written, s.Hosts[hostname].WrittenRecords())
	}
	return written
}

func (s EdgeState) PointerRecords() []edge.Record {
	var written []edge.Record
	for _, hostname := range s.Hostnames() {
		written = mergeRecords(written, s.Hosts[hostname].Written)
	}
	return written
}

func (s EdgeState) ManualRecords() []edge.Record {
	var manual []edge.Record
	for _, hostname := range s.Hostnames() {
		manual = mergeRecords(manual, s.Hosts[hostname].ManualRecords())
	}
	return manual
}

func (s EdgeState) Certificates() []provider.Certificate {
	var held []provider.Certificate
	for _, hostname := range s.Hostnames() {
		for _, cert := range s.Hosts[hostname].Certificates() {
			if !holds(held, cert) {
				held = append(held, cert)
			}
		}
	}
	return held
}

func (s EdgeState) Uses(id string) bool {
	if id == "" {
		return false
	}
	for _, host := range s.Hosts {
		if host.Certificate.ID == id {
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

func (s *EdgeState) Forget(hostname string) {
	delete(s.Hosts, hostname)
	if len(s.Hosts) == 0 {
		s.Hosts = nil
	}
}

func ReadWildcard(ctx context.Context, store records.Store) (Wildcard, error) {
	name := WildcardRecord(edge.ClassPreview)
	record, err := records.ReadOrEmpty(ctx, store, name)
	if err != nil {
		return Wildcard{}, fmt.Errorf("read %s: %w", name, err)
	}
	var held Wildcard
	if len(record.Bytes) == 0 {
		return held, nil
	}
	if err := json.Unmarshal(record.Bytes, &held); err != nil {
		return Wildcard{}, fmt.Errorf("read %s: %w", name, err)
	}
	return held, nil
}

func ProjectsServedOnPreview(ctx context.Context, records records.Store, baseDomain string) ([]string, error) {
	if baseDomain == "" {
		return nil, nil
	}
	under := EdgeStacksRecord(edge.ClassPreview)
	held, err := records.List(ctx, under)
	if err != nil {
		return nil, fmt.Errorf("read the projects served on %s: %w", edge.PreviewWildcard(baseDomain), err)
	}
	var served []string
	for _, record := range held {
		rest, named := record.Name.Under(under)
		if !named || len(record.Bytes) == 0 {
			continue
		}
		var state EdgeState
		if err := json.Unmarshal(record.Bytes, &state); err != nil {
			return nil, fmt.Errorf("read %s: %w", record.Name, err)
		}
		if state.Edge.ServedOnGlobalPreview(baseDomain) {
			served = append(served, rest[0])
		}
	}
	slices.Sort(served)
	return served, nil
}

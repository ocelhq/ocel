package stackrecords

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/router"
)

type EdgeState struct {
	Kind    edge.Kind                         `json:"kind,omitempty"`
	Edge    edge.StackState                   `json:"edge"`
	Routers map[router.Kind]router.StackState `json:"routers,omitempty"`
	Apps    map[string]router.Kind            `json:"apps,omitempty"`
	Hosts   map[string]HostnameState          `json:"hosts,omitempty"`
}

func (s *EdgeState) Pair(kind router.Kind, state router.StackState, apps map[string]router.Kind) {
	if len(apps) == 0 {
		return
	}
	if s.Routers == nil {
		s.Routers = map[router.Kind]router.StackState{}
	}
	s.Routers[kind] = state
	if s.Apps == nil {
		s.Apps = map[string]router.Kind{}
	}
	maps.Copy(s.Apps, apps)
}

type HostnameState struct {
	Edge        edge.Kind              `json:"edge,omitempty"`
	Certificate provider.Certificate   `json:"certificate,omitzero"`
	Superseded  []provider.Certificate `json:"superseded,omitempty"`
	Written     []edge.Record          `json:"written,omitempty"`
	Manual      []edge.Record          `json:"owed,omitempty"`
	Probe       ServeProbe             `json:"probe,omitzero"`

	ClientCertificateDigests []string  `json:"clientCertificateDigests,omitempty"`
	OriginCertificate        string    `json:"originCertificate,omitempty"`
	OriginCertificateExpires time.Time `json:"originCertificateExpires,omitzero"`
}

func (s *HostnameState) Supersede(cert provider.Certificate) {
	if !cert.Issued() || cert.ID == s.Certificate.ID || containsCertificate(s.Superseded, cert) {
		return
	}
	s.Superseded = append(s.Superseded, cert)
}

func (s HostnameState) Certificates() []provider.Certificate {
	issued := make([]provider.Certificate, 0, 1+len(s.Superseded))
	for _, cert := range append([]provider.Certificate{s.Certificate}, s.Superseded...) {
		if cert.Issued() && !containsCertificate(issued, cert) {
			issued = append(issued, cert)
		}
	}
	return issued
}

func containsCertificate(certificates []provider.Certificate, cert provider.Certificate) bool {
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
	At     int64       `json:"at,omitempty"`
	OK     bool        `json:"ok,omitempty"`
	Router router.Kind `json:"router,omitempty"`
}

func (p ServeProbe) IsAnsweredBy(kind router.Kind) bool { return p.Router != "" && p.Router == kind }

func (s HostnameState) ServedEdge() (edge.Kind, bool) { return s.Edge, s.Probe.OK }

type Wildcard struct {
	BaseDomain string        `json:"base_domain,omitempty"`
	Edge       edge.Kind     `json:"edge,omitempty"`
	Scope      string        `json:"scope,omitempty"`
	GrammarMin uint32        `json:"grammar_min,omitempty"`
	GrammarMax uint32        `json:"grammar_max,omitempty"`
	Host       HostnameState `json:"settled,omitzero"`
}

func (w Wildcard) Hostname() string { return edge.PreviewWildcard(w.BaseDomain) }

func (w Wildcard) IsRecorded() bool { return w.BaseDomain != "" }

func (s EdgeState) Host(hostname string) HostnameState { return s.Hosts[hostname] }

func (s EdgeState) Hostnames() []string { return slices.Sorted(maps.Keys(s.Hosts)) }

func (s EdgeState) Ready(hostname string, front edge.Kind, answering router.Kind) bool {
	host := s.Host(hostname)
	return host.Probe.OK && host.Edge == front && host.Probe.IsAnsweredBy(answering)
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
	var certificates []provider.Certificate
	for _, hostname := range s.Hostnames() {
		for _, cert := range s.Hosts[hostname].Certificates() {
			if !containsCertificate(certificates, cert) {
				certificates = append(certificates, cert)
			}
		}
	}
	return certificates
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

func mergeRecords(into, dnsRecords []edge.Record) []edge.Record {
	for _, rec := range dnsRecords {
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

func ReadWildcard(ctx context.Context, store keyvalue.Store) (Wildcard, error) {
	name := WildcardKey(environment.TierPreview)
	entry, err := keyvalue.ReadOrEmpty(ctx, store, name)
	if err != nil {
		return Wildcard{}, fmt.Errorf("read %s: %w", name, err)
	}
	var wildcard Wildcard
	if len(entry.Value) == 0 {
		return wildcard, nil
	}
	if err := json.Unmarshal(entry.Value, &wildcard); err != nil {
		return Wildcard{}, fmt.Errorf("read %s: %w", name, err)
	}
	return wildcard, nil
}

func ProjectsServedOnPreview(ctx context.Context, store keyvalue.Store, baseDomain string) ([]string, error) {
	if baseDomain == "" {
		return nil, nil
	}
	recorded, err := store.List(ctx, EdgeStacksPartition(environment.TierPreview))
	if err != nil {
		return nil, fmt.Errorf("read the projects served on %s: %w", edge.PreviewWildcard(baseDomain), err)
	}
	var served []string
	for _, entry := range recorded {
		if len(entry.Key.Path) != 1 || len(entry.Value) == 0 {
			continue
		}
		var state EdgeState
		if err := json.Unmarshal(entry.Value, &state); err != nil {
			return nil, fmt.Errorf("read %s: %w", entry.Key, err)
		}
		if state.Edge.ServedOnGlobalPreview(baseDomain) {
			served = append(served, entry.Key.Path[0])
		}
	}
	slices.Sort(served)
	return served, nil
}

package bootstrap

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	"github.com/ocelhq/ocel/platform/aws/provider/certs"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type Provisioned struct {
	Hostname    string        `json:"hostname"`
	Certificate string        `json:"certificate,omitempty"`
	Written     []edge.Record `json:"written,omitempty"`
	Owed        []edge.Record `json:"owed,omitempty"`
	Probe       certs.Probe   `json:"probe,omitzero"`
}

type Production struct {
	Certificate certs.Certificate `json:"certificate,omitzero"`
	Written     []edge.Record     `json:"written,omitempty"`
	Owed        []edge.Record     `json:"owed,omitempty"`
	Hosts       []Provisioned     `json:"hosts,omitempty"`
}

func ReadProduction(state edge.StackState) (Production, error) {
	raw := state[edge.StackKeyProductionDomains]
	if raw == "" {
		return Production{}, nil
	}
	var recorded Production
	if err := json.Unmarshal([]byte(raw), &recorded); err != nil {
		return Production{}, fmt.Errorf("parse the production domains recorded on this stack: %w", err)
	}
	return recorded, nil
}

func WithProduction(state edge.StackState, recorded Production) (edge.StackState, error) {
	next := maps.Clone(state)
	if next == nil {
		next = edge.StackState{}
	}
	if recorded.Certificate.ARN == "" && len(recorded.Hosts) == 0 && len(recorded.Written) == 0 && len(recorded.Owed) == 0 {
		delete(next, edge.StackKeyProductionDomains)
		return next, nil
	}
	payload, err := json.Marshal(recorded)
	if err != nil {
		return nil, fmt.Errorf("record the production domains of this stack: %w", err)
	}
	next[edge.StackKeyProductionDomains] = string(payload)
	return next, nil
}

func (p Production) Host(hostname string) Provisioned {
	for _, host := range p.Hosts {
		if host.Hostname == hostname {
			return host
		}
	}
	return Provisioned{Hostname: hostname}
}

func (p Production) Ready(hostname string, kind edge.Kind) bool {
	for _, host := range p.Hosts {
		if host.Hostname == hostname {
			return host.Probe.OK && host.Probe.Edge == kind
		}
	}
	return false
}

func (p Production) Hostnames() []string {
	names := make([]string, 0, len(p.Hosts))
	for _, host := range p.Hosts {
		names = append(names, host.Hostname)
	}
	return names
}

func (p Production) WithHost(host Provisioned) Production {
	next := p
	next.Hosts = slices.Clone(p.Hosts)
	for i, recorded := range next.Hosts {
		if recorded.Hostname == host.Hostname {
			next.Hosts[i] = host
			return next
		}
	}
	next.Hosts = append(next.Hosts, host)
	slices.SortFunc(next.Hosts, func(a, b Provisioned) int {
		if a.Hostname < b.Hostname {
			return -1
		}
		if a.Hostname > b.Hostname {
			return 1
		}
		return 0
	})
	return next
}

func (p Production) WithoutHost(hostname string) Production {
	next := p
	next.Hosts = slices.DeleteFunc(slices.Clone(p.Hosts), func(host Provisioned) bool {
		return host.Hostname == hostname
	})
	return next
}

func (p Production) Settlement() certs.Settlement {
	return certs.Settlement{Certificate: p.Certificate, Written: p.Written, Owed: p.Owed}
}

func (p Production) WithSettlement(settled certs.Settlement) Production {
	next := p
	next.Certificate, next.Written, next.Owed = settled.Certificate, settled.Written, settled.Owed
	return next
}

func (p Production) Uses(arn string) bool {
	return slices.ContainsFunc(p.Hosts, func(host Provisioned) bool { return host.Certificate == arn })
}

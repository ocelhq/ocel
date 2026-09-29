package project

import (
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

type Hostname struct {
	Name string
	App  string
}

func (p *Project) Hostnames(tier environmentv1.Tier) []Hostname {
	if tier == environmentv1.Tier_TIER_PREVIEW {
		if p.Domains.Preview == "" {
			return nil
		}
		return []Hostname{{Name: p.Domains.Preview}}
	}
	var hosts []Hostname
	seen := map[string]bool{}
	add := func(domains []string, app string) {
		for _, host := range domains {
			if seen[host] {
				continue
			}
			seen[host] = true
			hosts = append(hosts, Hostname{Name: host, App: app})
		}
	}
	add(p.Domains.Production, "")
	for _, app := range p.Apps {
		add(app.ProductionDomains, app.Name)
	}
	return hosts
}

func (p *Project) HostnameNames(tier environmentv1.Tier) []string {
	hosts := p.Hostnames(tier)
	named := make([]string, 0, len(hosts))
	for _, host := range hosts {
		named = append(named, host.Name)
	}
	return named
}

func (p *Project) ConfiguredHostnames(tier environmentv1.Tier) []*contractv1.ConfiguredHostname {
	hosts := p.Hostnames(tier)
	configured := make([]*contractv1.ConfiguredHostname, 0, len(hosts))
	for _, host := range hosts {
		configured = append(configured, &contractv1.ConfiguredHostname{Hostname: host.Name, App: host.App})
	}
	return configured
}

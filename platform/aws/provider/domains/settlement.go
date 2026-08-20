package domains

import (
	"slices"

	"github.com/ocelhq/ocel/platform/aws/provider/certs"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type Records struct {
	Written []edge.Record `json:"written,omitempty"`
	Owed    []edge.Record `json:"owed,omitempty"`
}

type Host struct {
	Hostname    string      `json:"hostname"`
	Certificate string      `json:"certificate,omitempty"`
	Records     Records     `json:"records,omitzero"`
	Probe       certs.Probe `json:"probe,omitzero"`
}

func (h Host) Serving() edge.Kind {
	if !h.Probe.OK {
		return ""
	}
	return h.Probe.Edge
}

type Settlement struct {
	Certificate certs.Certificate `json:"certificate,omitzero"`
	Validation  Records           `json:"validation,omitzero"`
	Hosts       []Host            `json:"hosts,omitempty"`
}

func (s Settlement) Empty() bool {
	return s.Certificate.ARN == "" && len(s.Hosts) == 0 &&
		len(s.Validation.Written) == 0 && len(s.Validation.Owed) == 0
}

func (s Settlement) Host(hostname string) Host {
	for _, host := range s.Hosts {
		if host.Hostname == hostname {
			return host
		}
	}
	return Host{Hostname: hostname}
}

func (s Settlement) Ready(hostname string, kind edge.Kind) bool {
	for _, host := range s.Hosts {
		if host.Hostname == hostname {
			return host.Probe.OK && host.Probe.Edge == kind
		}
	}
	return false
}

func (s Settlement) Hostnames() []string {
	names := make([]string, 0, len(s.Hosts))
	for _, host := range s.Hosts {
		names = append(names, host.Hostname)
	}
	return names
}

func (s Settlement) WithHost(host Host) Settlement {
	next := s
	next.Hosts = slices.Clone(s.Hosts)
	for i, recorded := range next.Hosts {
		if recorded.Hostname == host.Hostname {
			next.Hosts[i] = host
			return next
		}
	}
	next.Hosts = append(next.Hosts, host)
	slices.SortFunc(next.Hosts, func(a, b Host) int {
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

func (s Settlement) WithoutHost(hostname string) Settlement {
	next := s
	next.Hosts = slices.DeleteFunc(slices.Clone(s.Hosts), func(host Host) bool {
		return host.Hostname == hostname
	})
	return next
}

func (s Settlement) WithoutCertificate() Settlement {
	next := s
	next.Certificate, next.Validation = certs.Certificate{}, Records{}
	return next
}

func (s Settlement) Uses(arn string) bool {
	return slices.ContainsFunc(s.Hosts, func(host Host) bool { return host.Certificate == arn })
}

func (s Settlement) WrittenRecords() []edge.Record {
	var records []edge.Record
	for _, host := range s.Hosts {
		records = append(records, host.Records.Written...)
	}
	return append(records, s.Validation.Written...)
}

func (s Settlement) OwedRecords() []edge.Record {
	var records []edge.Record
	for _, host := range s.Hosts {
		records = append(records, host.Records.Owed...)
	}
	return append(records, s.Validation.Owed...)
}

package edge

import (
	"context"
	"fmt"
	"maps"
	"net/netip"
	"slices"
	"strings"
	"time"
)

type RecordType string

const (
	RecordTypeA     RecordType = "A"
	RecordTypeAAAA  RecordType = "AAAA"
	RecordTypeCNAME RecordType = "CNAME"
)

const ProxyPlaceholder = "100::"

type Record struct {
	Name    string     `json:"name"`
	Type    RecordType `json:"type"`
	Value   string     `json:"value"`
	Proxied bool       `json:"proxied,omitempty"`
}

func (r Record) String() string {
	return fmt.Sprintf("%s %s %s", r.Name, r.Type, r.Value)
}

func (r Record) Instruction() string {
	if r.Proxied {
		return fmt.Sprintf("add a DNS record at %s proxied through the edge", r.Name)
	}
	return fmt.Sprintf("add %s at %s pointing to %s", recordPhrase(r.Type), r.Name, r.Value)
}

func recordPhrase(t RecordType) string {
	switch t {
	case RecordTypeA:
		return "an A record"
	case RecordTypeAAAA:
		return "an AAAA record"
	case RecordTypeCNAME:
		return "a CNAME record"
	}
	return string(t) + " record"
}

func (r Record) ApexNote(zone string) string {
	if r.Type != RecordTypeCNAME || !apexOf(r.Name, zone) {
		return ""
	}
	return fmt.Sprintf(
		"%s is an apex name, and a CNAME cannot sit beside the NS and SOA records already there: point it at %s with your DNS provider's ALIAS or ANAME record, or with CNAME flattening",
		r.Name, r.Value,
	)
}

func apexOf(name, zone string) bool {
	if zone == "" {
		return strings.Count(name, ".") == 1
	}
	return strings.EqualFold(strings.TrimSuffix(name, "."), strings.TrimSuffix(zone, "."))
}

type DNSRecords interface {
	Ensure(ctx context.Context, records []Record, say func(string)) ([]Record, error)

	Delete(ctx context.Context, records []Record) error

	TTL() time.Duration
}

type DNSTarget struct {
	Kind           Kind
	ServesUnbound  bool
	ProxiesRecords bool
	Address        string
	AddressByHost  map[string]string
}

func (t DNSTarget) AddressFor(hostname string) string {
	if address := t.AddressByHost[hostname]; address != "" {
		return address
	}
	return t.Address
}

func (s *StackState) PublishAddress(hostname, address string) {
	if hostname == "" {
		return
	}
	if address == "" {
		if _, published := s.Addresses[hostname]; !published {
			return
		}
		addresses := maps.Clone(s.Addresses)
		delete(addresses, hostname)
		if len(addresses) == 0 {
			addresses = nil
		}
		s.Addresses = addresses
		return
	}
	addresses := maps.Clone(s.Addresses)
	if addresses == nil {
		addresses = map[string]string{}
	}
	addresses[hostname] = address
	s.Addresses = addresses
}

func TargetFor(e Edge, state StackState) DNSTarget {
	return TargetOf(e.Kind(), e.Facts(), state)
}

func TargetOf(kind Kind, facts Facts, state StackState) DNSTarget {
	return DNSTarget{
		Kind:           kind,
		ServesUnbound:  facts.ServesUnbound,
		ProxiesRecords: facts.ProxiesRecords,
		Address:        state.Address,
		AddressByHost:  state.Addresses,
	}
}

func Pointable(target DNSTarget, bound []string, hostname string) bool {
	return target.ServesUnbound || slices.Contains(bound, hostname)
}

func RecordsFor(target DNSTarget, hostnames []string) ([]Record, error) {
	records := make([]Record, 0, len(hostnames))
	for _, host := range hostnames {
		if host == "" || Loopback(host) {
			continue
		}
		address := target.AddressFor(host)
		switch {
		case address != "":
			record := addressRecord(host, address)
			record.Proxied = target.ProxiesRecords
			records = append(records, record)
		case target.ServesUnbound:
			records = append(records, Record{Name: host, Type: RecordTypeAAAA, Value: ProxyPlaceholder, Proxied: true})
		default:
			return nil, fmt.Errorf("nothing to point %s at: this deployment published no hostname or address to point it at", host)
		}
	}
	return records, nil
}

func Loopback(hostname string) bool {
	return ZoneOwns(strings.TrimSuffix(hostname, "."), "localhost")
}

func addressRecord(host, address string) Record {
	addr, err := netip.ParseAddr(address)
	if err != nil || addr.Zone() != "" {
		return Record{Name: host, Type: RecordTypeCNAME, Value: address}
	}
	addr = addr.Unmap()
	if addr.Is4() {
		return Record{Name: host, Type: RecordTypeA, Value: addr.String()}
	}
	return Record{Name: host, Type: RecordTypeAAAA, Value: addr.String()}
}

type Zone struct {
	ID   string
	Name string
}

func SelectZone(zones []Zone, hostname, named string) (Zone, error) {
	if named != "" {
		for _, z := range zones {
			if !strings.EqualFold(z.Name, named) {
				continue
			}
			if !ZoneOwns(hostname, z.Name) {
				return Zone{}, fmt.Errorf("zone %q does not own %q — name the zone %q belongs to, or drop the zone and let it be matched", named, hostname, hostname)
			}
			return z, nil
		}
		return Zone{}, fmt.Errorf("no zone named %q is reachable with these credentials", named)
	}
	var best Zone
	for _, z := range zones {
		if ZoneOwns(hostname, z.Name) && len(z.Name) > len(best.Name) {
			best = z
		}
	}
	if best.ID == "" {
		return Zone{}, fmt.Errorf("no zone reachable with these credentials owns %q — host its zone there, or name the zone to write into", hostname)
	}
	return best, nil
}

func ZoneOwns(hostname, zone string) bool {
	host, owner := strings.ToLower(hostname), strings.ToLower(zone)
	return host == owner || strings.HasSuffix(host, "."+owner)
}

func (s *StackState) RecordWrites(records []Record) {
	kept := slices.CompactFunc(sortedRecords(slices.Clone(records)), func(a, b Record) bool { return a == b })
	if len(kept) == 0 {
		kept = nil
	}
	s.Records = kept
}

func sortedRecords(records []Record) []Record {
	slices.SortFunc(records, func(a, b Record) int {
		if c := strings.Compare(a.Name, b.Name); c != 0 {
			return c
		}
		if c := strings.Compare(string(a.Type), string(b.Type)); c != 0 {
			return c
		}
		return strings.Compare(a.Value, b.Value)
	})
	return records
}

func Unwritten(wanted, written []Record) []Record {
	var unwritten []Record
	for _, rec := range wanted {
		if !slices.Contains(written, rec) {
			unwritten = append(unwritten, rec)
		}
	}
	return unwritten
}

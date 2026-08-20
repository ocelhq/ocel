package edge

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
)

type RecordType string

const (
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
		return fmt.Sprintf("add a proxied (orange cloud) DNS record at %s", r.Name)
	}
	return fmt.Sprintf("add a %s record at %s pointing to %s", r.Type, r.Name, r.Value)
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

type DNSWriter interface {
	EnsureRecords(ctx context.Context, records []Record, say func(string)) ([]Record, error)

	DeleteRecords(ctx context.Context, records []Record) error
}

type TTLBound interface {
	RecordTTL() time.Duration
}

func WriteTTL(writer DNSWriter) time.Duration {
	bound, ok := writer.(TTLBound)
	if !ok {
		return 0
	}
	return bound.RecordTTL()
}

type ZoneFinder interface {
	ZoneOf(ctx context.Context, hostname string) (Zone, error)
}

type DNSTarget struct {
	Kind          Kind
	ServesUnbound bool
	Front         string
	FrontByHost   map[string]string
}

func (t DNSTarget) FrontFor(hostname string) string {
	if front := t.FrontByHost[hostname]; front != "" {
		return front
	}
	return t.Front
}

func (s *StackState) PublishFront(hostname, front string) {
	if hostname == "" {
		return
	}
	if front == "" {
		if _, held := s.Fronts[hostname]; !held {
			return
		}
		fronts := maps.Clone(s.Fronts)
		delete(fronts, hostname)
		if len(fronts) == 0 {
			fronts = nil
		}
		s.Fronts = fronts
		return
	}
	fronts := maps.Clone(s.Fronts)
	if fronts == nil {
		fronts = map[string]string{}
	}
	fronts[hostname] = front
	s.Fronts = fronts
}

func TargetFor(e Edge, state StackState) DNSTarget {
	return TargetOf(e.Kind(), e.Facts().ServesUnbound, state)
}

func TargetOf(kind Kind, servesUnbound bool, state StackState) DNSTarget {
	return DNSTarget{Kind: kind, ServesUnbound: servesUnbound, Front: state.Front, FrontByHost: state.Fronts}
}

func Pointable(target DNSTarget, bound []string, hostname string) bool {
	return target.ServesUnbound || slices.Contains(bound, hostname)
}

func RecordsFor(target DNSTarget, hostnames []string) ([]Record, error) {
	records := make([]Record, 0, len(hostnames))
	for _, host := range hostnames {
		if host == "" {
			continue
		}
		if target.ServesUnbound {
			records = append(records, Record{Name: host, Type: RecordTypeAAAA, Value: ProxyPlaceholder, Proxied: true})
			continue
		}
		front := target.FrontFor(host)
		if front == "" {
			return nil, fmt.Errorf("nothing to point %s at: the %s edge published no hostname for this deployment", host, target.Kind)
		}
		records = append(records, Record{Name: host, Type: RecordTypeCNAME, Value: front})
	}
	return records, nil
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
	var owed []Record
	for _, rec := range wanted {
		if !slices.Contains(written, rec) {
			owed = append(owed, rec)
		}
	}
	return owed
}

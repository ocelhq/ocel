package edge

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
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

type DNSWriter interface {
	EnsureRecords(ctx context.Context, records []Record, say func(string)) ([]Record, error)

	DeleteRecords(ctx context.Context, records []Record) error
}

type DNSTarget struct {
	Kind  Kind
	Front string
}

func RecordsFor(target DNSTarget, hostnames []string) ([]Record, error) {
	records := make([]Record, 0, len(hostnames))
	for _, host := range hostnames {
		if host == "" {
			continue
		}
		if target.Kind == KindCloudflare {
			records = append(records, Record{Name: host, Type: RecordTypeAAAA, Value: ProxyPlaceholder, Proxied: true})
			continue
		}
		if target.Front == "" {
			return nil, fmt.Errorf("nothing to point %s at: the %s edge published no hostname for this deployment", host, target.Kind)
		}
		records = append(records, Record{Name: host, Type: RecordTypeCNAME, Value: target.Front})
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

const StackKeyRecords = "records"

func WrittenRecords(state StackState) ([]Record, error) {
	raw := state[StackKeyRecords]
	if raw == "" {
		return nil, nil
	}
	var records []Record
	if err := json.Unmarshal([]byte(raw), &records); err != nil {
		return nil, fmt.Errorf("parse the DNS records recorded on this stack: %w", err)
	}
	return records, nil
}

func WithWrittenRecords(state StackState, records []Record) (StackState, error) {
	next := StackState{}
	for k, v := range state {
		next[k] = v
	}
	kept := slices.Clone(records)
	kept = slices.CompactFunc(sortedRecords(kept), func(a, b Record) bool { return a == b })
	if len(kept) == 0 {
		delete(next, StackKeyRecords)
		return next, nil
	}
	payload, err := json.Marshal(kept)
	if err != nil {
		return nil, fmt.Errorf("record the DNS records this stack wrote: %w", err)
	}
	next[StackKeyRecords] = string(payload)
	return next, nil
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

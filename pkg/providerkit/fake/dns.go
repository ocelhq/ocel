package fake

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type DNS struct {
	mu      sync.Mutex
	writers map[string]*DNSRecords
	fronts  []edge.Kind
}

const KindZone provider.DNSKind = "zone"

func NewDNS() *DNS { return &DNS{writers: map[string]*DNSRecords{}} }

func (d *DNS) Open(kind provider.DNSKind, zone string, front edge.Kind) (edge.DNSRecords, error) {
	if kind != KindZone {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"the reference provider writes no dns %q; it writes %s", kind, KindZone)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.fronts = append(d.fronts, front)
	writer, open := d.writers[zone]
	if !open {
		writer = &DNSRecords{zone: zone}
		d.writers[zone] = writer
	}
	return writer, nil
}

func (d *DNS) Fronts() []edge.Kind {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.fronts)
}

func (d *DNS) Zone(zone string) *DNSRecords {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.writers[zone]
}

type DNSRecords struct {
	mu      sync.Mutex
	zone    string
	records []edge.Record
	refusal error
}

func (w *DNSRecords) Refuse(err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.refusal = err
}

func (w *DNSRecords) Records() []edge.Record {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.records)
}

func (w *DNSRecords) TTL() time.Duration { return 60 * time.Second }

func (w *DNSRecords) Ensure(_ context.Context, records []edge.Record, say func(string)) ([]edge.Record, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.refusal != nil {
		return nil, w.refusal
	}
	for _, record := range records {
		if !slices.Contains(w.records, record) {
			w.records = append(w.records, record)
		}
		if say != nil {
			say("Wrote " + record.String())
		}
	}
	return slices.Clone(records), nil
}

func (w *DNSRecords) Delete(_ context.Context, records []edge.Record) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.refusal != nil {
		return w.refusal
	}
	w.records = slices.DeleteFunc(w.records, func(held edge.Record) bool {
		return slices.Contains(records, held)
	})
	return nil
}

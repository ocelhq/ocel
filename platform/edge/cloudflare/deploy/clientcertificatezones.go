package cloudflare

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

const clientCertificateZonesRecord = "client-certificate-zones"

type recordedZone struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type clientCertificateZones struct {
	Zones []recordedZone `json:"zones"`
}

func clientCertificateZonesKey(tier environment.Tier) records.Key {
	return stackrecords.EdgeStacksPartition(tier).Key(string(Kind), clientCertificateZonesRecord)
}

func decodeClientCertificateZones(entry records.Entry) (clientCertificateZones, error) {
	var recorded clientCertificateZones
	if len(entry.Value) == 0 {
		return recorded, nil
	}
	if err := json.Unmarshal(entry.Value, &recorded); err != nil {
		return clientCertificateZones{}, fmt.Errorf("read %s: %w", entry.Key, err)
	}
	return recorded, nil
}

func (p *cloudflare) requireKeyValues(doing string) (records.Store, error) {
	if p.keyValues == nil {
		return nil, fmt.Errorf("%s: the cloudflare edge was opened with no record store to keep the zones its client certificates are in", doing)
	}
	return p.keyValues, nil
}

func (p *cloudflare) recordClientCertificateZone(ctx context.Context, tier environment.Tier, zone recordedZone) error {
	doing := fmt.Sprintf("record that tier %s presents a client certificate in zone %s", tier, zone.Name)
	store, err := p.requireKeyValues(doing)
	if err != nil {
		return err
	}
	err = records.Change(ctx, store, clientCertificateZonesKey(tier), func(entry records.Entry) ([]byte, bool, error) {
		recorded, err := decodeClientCertificateZones(entry)
		if err != nil || slices.Contains(recorded.Zones, zone) {
			return nil, false, err
		}
		recorded.Zones = append(recorded.Zones, zone)
		value, err := json.Marshal(recorded)
		return value, true, err
	})
	if err != nil {
		return fmt.Errorf("%s: %w", doing, err)
	}
	return nil
}

func (p *cloudflare) readClientCertificateZones(ctx context.Context, tier environment.Tier) ([]recordedZone, error) {
	doing := fmt.Sprintf("read the zones tier %s presents client certificates in", tier)
	store, err := p.requireKeyValues(doing)
	if err != nil {
		return nil, err
	}
	entry, err := records.ReadOrEmpty(ctx, store, clientCertificateZonesKey(tier))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", doing, err)
	}
	recorded, err := decodeClientCertificateZones(entry)
	return recorded.Zones, err
}

func (p *cloudflare) forgetClientCertificateZones(ctx context.Context, tier environment.Tier) error {
	store, err := p.requireKeyValues("forget the zones tier " + string(tier) + " presents client certificates in")
	if err != nil {
		return err
	}
	return records.Forget(ctx, store, clientCertificateZonesKey(tier))
}

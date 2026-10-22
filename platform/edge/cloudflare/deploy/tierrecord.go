package cloudflare

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

const tierRecordName = "bootstrap"

type tierRecord struct {
	AccountID              string         `json:"accountId"`
	ClientCertificateZones []recordedZone `json:"clientCertificateZones,omitempty"`
}

type recordedZone struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func tierRecordKey(tier environment.Tier) records.Key {
	return stackrecords.EdgeStacksPartition(tier).Key(string(Kind), tierRecordName)
}

func decodeTierRecord(entry records.Entry) (tierRecord, error) {
	var recorded tierRecord
	if len(entry.Value) == 0 {
		return recorded, nil
	}
	if err := json.Unmarshal(entry.Value, &recorded); err != nil {
		return tierRecord{}, fmt.Errorf("read %s: %w", entry.Key, err)
	}
	return recorded, nil
}

func (o *resolvedOrigin) requireRecords(doing string) (records.Store, error) {
	if o.Records == nil {
		return nil, fmt.Errorf("%s: the cloudflare edge was opened with no record store to keep what this origin records of its tiers", doing)
	}
	return o.Records, nil
}

func (o *resolvedOrigin) readTierRecord(ctx context.Context, tier environment.Tier) (tierRecord, error) {
	doing := fmt.Sprintf("read what this origin recorded of tier %s on Cloudflare", tier)
	store, err := o.requireRecords(doing)
	if err != nil {
		return tierRecord{}, err
	}
	entry, err := records.ReadOrEmpty(ctx, store, tierRecordKey(tier))
	if err != nil {
		return tierRecord{}, fmt.Errorf("%s: %w", doing, err)
	}
	return decodeTierRecord(entry)
}

func (o *resolvedOrigin) changeTierRecord(ctx context.Context, tier environment.Tier, doing string, change func(recorded *tierRecord) (bool, error)) error {
	store, err := o.requireRecords(doing)
	if err != nil {
		return err
	}
	return records.Change(ctx, store, tierRecordKey(tier), func(entry records.Entry) ([]byte, bool, error) {
		recorded, err := decodeTierRecord(entry)
		if err != nil {
			return nil, false, err
		}
		if changed, err := change(&recorded); err != nil || !changed {
			return nil, false, err
		}
		value, err := json.Marshal(recorded)
		return value, true, err
	})
}

func (o *resolvedOrigin) forgetTierRecord(ctx context.Context, tier environment.Tier) error {
	store, err := o.requireRecords(fmt.Sprintf("forget what this origin recorded of tier %s on Cloudflare", tier))
	if err != nil {
		return err
	}
	if err := records.Forget(ctx, store, tierRecordKey(tier)); err != nil {
		return err
	}
	o.forgetAccount(tier)
	return nil
}

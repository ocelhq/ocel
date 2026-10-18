package stackrecords

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"

	"github.com/ocelhq/ocel/pkg/edge/gate"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/seal"
)

var GatesAssociatedData = seal.Bind("gates")

type GateRecord struct {
	Gate     gate.Gate `json:"gate"`
	Password string    `json:"password"`
	Enabled  bool      `json:"enabled,omitempty"`
	Pending  bool      `json:"pending,omitempty"`
}

func (r GateRecord) GateInForce() *gate.Gate {
	if !r.Enabled {
		return nil
	}
	gate := r.Gate
	return &gate
}

func ReadGates(ctx context.Context, store records.Store, cipher seal.Cipher, tier environment.Tier) (map[string]GateRecord, error) {
	recorded, err := records.ReadOrEmpty(ctx, store, NewGatesRecordKey(tier))
	if err != nil {
		return nil, fmt.Errorf("read the %s gate keys: %w", tier, err)
	}
	return openGates(ctx, cipher, tier, recorded)
}

func ChangeGates(ctx context.Context, store records.Store, cipher seal.Cipher, tier environment.Tier, apply func(gates map[string]GateRecord) error) error {
	return records.Change(ctx, store, NewGatesRecordKey(tier), func(recorded records.Entry) ([]byte, bool, error) {
		gates, err := openGates(ctx, cipher, tier, recorded)
		if err != nil {
			return nil, false, err
		}
		if gates == nil {
			gates = map[string]GateRecord{}
		}
		before := cloneRecords(gates)
		if err := apply(gates); err != nil {
			return nil, false, err
		}
		if reflect.DeepEqual(before, gates) {
			return nil, false, nil
		}
		value, err := sealGates(ctx, cipher, tier, gates)
		if err != nil {
			return nil, false, err
		}
		return value, true, nil
	})
}

func cloneRecords(records map[string]GateRecord) map[string]GateRecord {
	cloned := make(map[string]GateRecord, len(records))
	for slug, record := range records {
		record.Gate.BypassSecrets = slices.Clone(record.Gate.BypassSecrets)
		record.Gate.AllowOptions = slices.Clone(record.Gate.AllowOptions)
		cloned[slug] = record
	}
	return cloned
}

func openGates(ctx context.Context, cipher seal.Cipher, tier environment.Tier, recorded records.Entry) (map[string]GateRecord, error) {
	if len(recorded.Value) == 0 {
		return nil, nil
	}
	var sealed []byte
	if err := json.Unmarshal(recorded.Value, &sealed); err != nil {
		return nil, fmt.Errorf("read the %s gate keys: %w", tier, err)
	}
	plaintext, err := cipher.Open(ctx, tier, GatesAssociatedData, sealed)
	if err != nil {
		return nil, fmt.Errorf("open the %s gate keys: %w", tier, err)
	}
	var gates map[string]GateRecord
	if err := json.Unmarshal(plaintext, &gates); err != nil {
		return nil, fmt.Errorf("read the %s gate keys: %w", tier, err)
	}
	return gates, nil
}

func sealGates(ctx context.Context, cipher seal.Cipher, tier environment.Tier, records map[string]GateRecord) ([]byte, error) {
	plaintext, err := json.Marshal(records)
	if err != nil {
		return nil, fmt.Errorf("record the %s gate keys: %w", tier, err)
	}
	sealed, err := cipher.Seal(ctx, tier, GatesAssociatedData, plaintext)
	if err != nil {
		return nil, fmt.Errorf("seal the %s gate keys: %w", tier, err)
	}
	value, err := json.Marshal(sealed)
	if err != nil {
		return nil, fmt.Errorf("record the %s gate keys: %w", tier, err)
	}
	return value, nil
}

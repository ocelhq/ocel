package stackrecords

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"

	"github.com/ocelhq/ocel/pkg/edge/previewgate"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/seal"
)

var PreviewGatesAssociatedData = seal.AssociatedData{{Name: "record", Value: "preview-gates"}}

type PreviewGateRecord struct {
	Gate     previewgate.Gate `json:"gate"`
	Password string           `json:"password"`
	Enabled  bool             `json:"enabled,omitempty"`
	Pending  bool             `json:"pending,omitempty"`
}

func (r PreviewGateRecord) GateInForce() *previewgate.Gate {
	if !r.Enabled {
		return nil
	}
	gate := r.Gate
	return &gate
}

func ReadPreviewGates(ctx context.Context, store keyvalue.Store, cipher seal.Cipher) (map[string]PreviewGateRecord, error) {
	recorded, err := keyvalue.ReadOrEmpty(ctx, store, NewPreviewGatesRecordKey())
	if err != nil {
		return nil, fmt.Errorf("read the keys that gate previews: %w", err)
	}
	return openPreviewGates(ctx, cipher, recorded)
}

func ReadPreviewGate(ctx context.Context, store keyvalue.Store, cipher seal.Cipher, slug string) (*previewgate.Gate, error) {
	records, err := ReadPreviewGates(ctx, store, cipher)
	if err != nil {
		return nil, err
	}
	return records[slug].GateInForce(), nil
}

func ChangePreviewGates(ctx context.Context, store keyvalue.Store, cipher seal.Cipher, apply func(records map[string]PreviewGateRecord) error) error {
	return keyvalue.Change(ctx, store, NewPreviewGatesRecordKey(), func(recorded keyvalue.Entry) ([]byte, bool, error) {
		records, err := openPreviewGates(ctx, cipher, recorded)
		if err != nil {
			return nil, false, err
		}
		if records == nil {
			records = map[string]PreviewGateRecord{}
		}
		before := cloneRecords(records)
		if err := apply(records); err != nil {
			return nil, false, err
		}
		if reflect.DeepEqual(before, records) {
			return nil, false, nil
		}
		value, err := sealPreviewGates(ctx, cipher, records)
		if err != nil {
			return nil, false, err
		}
		return value, true, nil
	})
}

func cloneRecords(records map[string]PreviewGateRecord) map[string]PreviewGateRecord {
	cloned := make(map[string]PreviewGateRecord, len(records))
	for slug, record := range records {
		record.Gate.BypassSecrets = slices.Clone(record.Gate.BypassSecrets)
		record.Gate.AllowOptions = slices.Clone(record.Gate.AllowOptions)
		cloned[slug] = record
	}
	return cloned
}

func openPreviewGates(ctx context.Context, cipher seal.Cipher, recorded keyvalue.Entry) (map[string]PreviewGateRecord, error) {
	if len(recorded.Value) == 0 {
		return nil, nil
	}
	var sealed []byte
	if err := json.Unmarshal(recorded.Value, &sealed); err != nil {
		return nil, fmt.Errorf("read the keys that gate previews: %w", err)
	}
	plaintext, err := cipher.Open(ctx, environment.TierPreview, PreviewGatesAssociatedData, sealed)
	if err != nil {
		return nil, fmt.Errorf("open the keys that gate previews: %w", err)
	}
	var records map[string]PreviewGateRecord
	if err := json.Unmarshal(plaintext, &records); err != nil {
		return nil, fmt.Errorf("read the keys that gate previews: %w", err)
	}
	return records, nil
}

func sealPreviewGates(ctx context.Context, cipher seal.Cipher, records map[string]PreviewGateRecord) ([]byte, error) {
	plaintext, err := json.Marshal(records)
	if err != nil {
		return nil, fmt.Errorf("record the keys that gate previews: %w", err)
	}
	sealed, err := cipher.Seal(ctx, environment.TierPreview, PreviewGatesAssociatedData, plaintext)
	if err != nil {
		return nil, fmt.Errorf("seal the keys that gate previews: %w", err)
	}
	value, err := json.Marshal(sealed)
	if err != nil {
		return nil, fmt.Errorf("record the keys that gate previews: %w", err)
	}
	return value, nil
}

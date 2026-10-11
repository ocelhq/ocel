package alb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/ocelhq/ocel/pkg/edge/previewgate"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/seal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/gcp/gateplugin"
)

const (
	gateRecordsSegment  = "gates"
	gatePublicationName = "gate-publication"
	gateRecordAttempts  = 10
)

type gateRecord struct {
	Hosts []string         `json:"hosts,omitempty"`
	Gate  previewgate.Gate `json:"gate"`
}

type gatePublication struct {
	Main      gateVersion   `json:"main"`
	Before    []gateVersion `json:"before,omitempty"`
	Confirmed bool          `json:"confirmed,omitempty"`
}

func gatePartition() keyvalue.Partition {
	return stackrecords.EdgeStacksPartition(environment.TierPreview)
}

func gateRecordKey(slug string) keyvalue.Key {
	return gatePartition().Key(string(Kind), gateRecordsSegment, slug)
}

func gatePublicationKey() keyvalue.Key {
	return gatePartition().Key(string(Kind), gatePublicationName)
}

func gateRecordData(slug string) seal.AssociatedData {
	return seal.AssociatedData{{Name: "record", Value: "alb-preview-gate"}, {Name: "slug", Value: slug}}
}

var gatePublicationData = seal.AssociatedData{{Name: "record", Value: "alb-preview-gate-publication"}}

func (e *Edge) sealRecord(ctx context.Context, bound seal.AssociatedData, record any) (json.RawMessage, error) {
	plaintext, err := json.Marshal(record)
	if err != nil {
		return nil, fmt.Errorf("encode what the %s edge keeps about the preview gate: %w", Kind, err)
	}
	sealed, err := e.deps.Cipher.Seal(ctx, environment.TierPreview, bound, plaintext)
	if err != nil {
		return nil, fmt.Errorf("seal what the %s edge keeps about the preview gate: %w", Kind, err)
	}
	return json.Marshal(sealed)
}

func (e *Edge) openRecord(ctx context.Context, bound seal.AssociatedData, stored json.RawMessage, into any) error {
	var sealed []byte
	if err := json.Unmarshal(stored, &sealed); err != nil {
		return fmt.Errorf("read what the %s edge keeps about the preview gate: %w", Kind, err)
	}
	plaintext, err := e.deps.Cipher.Open(ctx, environment.TierPreview, bound, sealed)
	if err != nil {
		return fmt.Errorf("open what the %s edge keeps about the preview gate: %w", Kind, err)
	}
	if err := json.Unmarshal(plaintext, into); err != nil {
		return fmt.Errorf("read what the %s edge keeps about the preview gate: %w", Kind, err)
	}
	return nil
}

func (e *Edge) readGateRecord(ctx context.Context, slug string) (gateRecord, bool, error) {
	entry, err := keyvalue.ReadOrEmpty(ctx, e.deps.KeyValues, gateRecordKey(slug))
	if err != nil {
		return gateRecord{}, false, fmt.Errorf("read the preview gate of %s on the %s edge: %w", slug, Kind, err)
	}
	if len(entry.Value) == 0 {
		return gateRecord{}, false, nil
	}
	var record gateRecord
	if err := e.openRecord(ctx, gateRecordData(slug), entry.Value, &record); err != nil {
		return gateRecord{}, false, err
	}
	return record, true, nil
}

func (e *Edge) changeGateRecord(ctx context.Context, slug string, change func(recorded gateRecord, found bool) (next gateRecord, keep bool)) (bool, error) {
	key := gateRecordKey(slug)
	for range gateRecordAttempts {
		entry, err := keyvalue.ReadOrEmpty(ctx, e.deps.KeyValues, key)
		if err != nil {
			return false, fmt.Errorf("read the preview gate of %s on the %s edge: %w", slug, Kind, err)
		}
		var recorded gateRecord
		found := len(entry.Value) > 0
		if found {
			if err := e.openRecord(ctx, gateRecordData(slug), entry.Value, &recorded); err != nil {
				return false, err
			}
		}
		next, keep := change(recorded, found)
		switch {
		case !keep && !found:
			return false, nil
		case !keep:
			return true, keyvalue.Forget(ctx, e.deps.KeyValues, key)
		case found && sameGateRecord(recorded, next):
			return false, nil
		}
		if entry.Value, err = e.sealRecord(ctx, gateRecordData(slug), next); err != nil {
			return false, err
		}
		_, err = e.deps.KeyValues.Write(ctx, entry)
		if err == nil {
			return true, nil
		}
		if !errors.Is(err, keyvalue.ErrStale) {
			return false, fmt.Errorf("record the preview gate of %s on the %s edge: %w", slug, Kind, err)
		}
	}
	return false, fmt.Errorf("record the preview gate of %s on the %s edge: it was rewritten between every read of it and the write that followed", slug, Kind)
}

func sameGateRecord(a, b gateRecord) bool {
	left, _ := json.Marshal(a)
	right, _ := json.Marshal(b)
	return string(left) == string(right)
}

func (e *Edge) listGateEntries(ctx context.Context) ([]keyvalue.Entry, error) {
	entries, err := e.deps.KeyValues.List(ctx, gatePartition(), string(Kind), gateRecordsSegment)
	if err != nil {
		return nil, fmt.Errorf("list the preview gates the %s edge runs: %w", Kind, err)
	}
	return entries, nil
}

func (e *Edge) encodeGateConfig(ctx context.Context) (string, error) {
	entries, err := e.listGateEntries(ctx)
	if err != nil {
		return "", err
	}
	hosts := map[string]string{}
	gates := make(map[string]previewgate.Gate, len(entries))
	for _, entry := range entries {
		slug := entry.Key.Path[len(entry.Key.Path)-1]
		var record gateRecord
		if err := e.openRecord(ctx, gateRecordData(slug), entry.Value, &record); err != nil {
			return "", err
		}
		gates[slug] = record.Gate
		for _, host := range record.Hosts {
			hosts[host] = slug
		}
	}
	return gateplugin.EncodeConfig(hosts, gates), nil
}

func (e *Edge) readGatePublication(ctx context.Context) (gatePublication, bool, error) {
	_, publication, found, err := e.readGatePublicationEntry(ctx)
	return publication, found, err
}

func (e *Edge) readGatePublicationEntry(ctx context.Context) (keyvalue.Entry, gatePublication, bool, error) {
	entry, err := keyvalue.ReadOrEmpty(ctx, e.deps.KeyValues, gatePublicationKey())
	if err != nil {
		return keyvalue.Entry{}, gatePublication{}, false, fmt.Errorf("read what the %s edge's gate plugin runs: %w", Kind, err)
	}
	if len(entry.Value) == 0 {
		return entry, gatePublication{}, false, nil
	}
	var publication gatePublication
	if err := e.openRecord(ctx, gatePublicationData, entry.Value, &publication); err != nil {
		return keyvalue.Entry{}, gatePublication{}, false, err
	}
	return entry, publication, true, nil
}

func (e *Edge) writeGatePublication(ctx context.Context, entry keyvalue.Entry, publication gatePublication) (keyvalue.Entry, error) {
	var err error
	if entry.Value, err = e.sealRecord(ctx, gatePublicationData, publication); err != nil {
		return keyvalue.Entry{}, err
	}
	if entry.Revision, err = e.deps.KeyValues.Write(ctx, entry); err != nil {
		if errors.Is(err, keyvalue.ErrStale) {
			return keyvalue.Entry{}, err
		}
		return keyvalue.Entry{}, fmt.Errorf("record what the %s edge's gate plugin runs: %w", Kind, err)
	}
	return entry, nil
}

func (e *Edge) forgetGates(ctx context.Context) error {
	entries, err := e.listGateEntries(ctx)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := keyvalue.Forget(ctx, e.deps.KeyValues, entry.Key); err != nil {
			return fmt.Errorf("forget the preview gate %s on the %s edge: %w", entry.Key, Kind, err)
		}
	}
	return keyvalue.Forget(ctx, e.deps.KeyValues, gatePublicationKey())
}

package alb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/ocelhq/ocel/pkg/edge/previewgate"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/seal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
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
	Main   gateVersion  `json:"main"`
	Before *gateVersion `json:"before,omitempty"`
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

func (e *Edge) changeGateRecord(ctx context.Context, slug string, change func(held gateRecord, found bool) (next gateRecord, keep bool)) (bool, error) {
	key := gateRecordKey(slug)
	for range gateRecordAttempts {
		entry, err := keyvalue.ReadOrEmpty(ctx, e.deps.KeyValues, key)
		if err != nil {
			return false, fmt.Errorf("read the preview gate of %s on the %s edge: %w", slug, Kind, err)
		}
		var held gateRecord
		found := len(entry.Value) > 0
		if found {
			if err := e.openRecord(ctx, gateRecordData(slug), entry.Value, &held); err != nil {
				return false, err
			}
		}
		next, keep := change(held, found)
		switch {
		case !keep && !found:
			return false, nil
		case !keep:
			return true, keyvalue.Forget(ctx, e.deps.KeyValues, key)
		case found && sameGateRecord(held, next):
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

type gateRecords struct {
	records   map[string]gateRecord
	revisions map[string]keyvalue.Revision
}

func (e *Edge) listGateEntries(ctx context.Context) ([]keyvalue.Entry, error) {
	entries, err := e.deps.KeyValues.List(ctx, gatePartition(), string(Kind), gateRecordsSegment)
	if err != nil {
		return nil, fmt.Errorf("list the preview gates the %s edge runs: %w", Kind, err)
	}
	return entries, nil
}

func revisionsOf(entries []keyvalue.Entry) map[string]keyvalue.Revision {
	revisions := make(map[string]keyvalue.Revision, len(entries))
	for _, entry := range entries {
		revisions[entry.Key.Path[len(entry.Key.Path)-1]] = entry.Revision
	}
	return revisions
}

func (e *Edge) openGateRecords(ctx context.Context, entries []keyvalue.Entry) (gateRecords, error) {
	opened := gateRecords{records: map[string]gateRecord{}, revisions: revisionsOf(entries)}
	for _, entry := range entries {
		slug := entry.Key.Path[len(entry.Key.Path)-1]
		var record gateRecord
		if err := e.openRecord(ctx, gateRecordData(slug), entry.Value, &record); err != nil {
			return gateRecords{}, err
		}
		opened.records[slug] = record
	}
	return opened, nil
}

func (r gateRecords) encodeConfig(encode func(hosts map[string]string, gates map[string]previewgate.Gate) string) string {
	hosts := map[string]string{}
	gates := make(map[string]previewgate.Gate, len(r.records))
	for _, slug := range slices.Sorted(maps.Keys(r.records)) {
		gates[slug] = r.records[slug].Gate
		for _, host := range r.records[slug].Hosts {
			hosts[host] = slug
		}
	}
	return encode(hosts, gates)
}

func (e *Edge) readGatePublication(ctx context.Context) (gatePublication, bool, error) {
	entry, err := keyvalue.ReadOrEmpty(ctx, e.deps.KeyValues, gatePublicationKey())
	if err != nil {
		return gatePublication{}, false, fmt.Errorf("read what the %s edge's gate plugin runs: %w", Kind, err)
	}
	if len(entry.Value) == 0 {
		return gatePublication{}, false, nil
	}
	var publication gatePublication
	if err := e.openRecord(ctx, gatePublicationData, entry.Value, &publication); err != nil {
		return gatePublication{}, false, err
	}
	return publication, true, nil
}

func (e *Edge) writeGatePublication(ctx context.Context, publication gatePublication) error {
	entry, err := keyvalue.ReadOrEmpty(ctx, e.deps.KeyValues, gatePublicationKey())
	if err != nil {
		return fmt.Errorf("read what the %s edge's gate plugin runs: %w", Kind, err)
	}
	if entry.Value, err = e.sealRecord(ctx, gatePublicationData, publication); err != nil {
		return err
	}
	if _, err := e.deps.KeyValues.Write(ctx, entry); err != nil {
		return fmt.Errorf("record what the %s edge's gate plugin runs: %w", Kind, err)
	}
	return nil
}

func (e *Edge) forgetGates(ctx context.Context) error {
	entries, err := e.listGateEntries(ctx)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := keyvalue.Forget(ctx, e.deps.KeyValues, entry.Key); err != nil {
			return fmt.Errorf("release the preview gate %s on the %s edge: %w", entry.Key, Kind, err)
		}
	}
	return keyvalue.Forget(ctx, e.deps.KeyValues, gatePublicationKey())
}

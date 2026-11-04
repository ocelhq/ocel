package alb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/ocelhq/ocel/pkg/edge/gate"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/seal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/gcp/gateplugin"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/albresources"
)

const (
	gateRecordsSegment  = "gate-records"
	gatePublicationName = "gate-plugin"
	gateRecordAttempts  = 10
)

type gateRecord struct {
	Hosts []string  `json:"hosts,omitempty"`
	Gate  gate.Gate `json:"gate"`
}

type gatePublication struct {
	Main      albresources.GateVersion   `json:"main"`
	Before    []albresources.GateVersion `json:"before,omitempty"`
	Confirmed bool                       `json:"confirmed,omitempty"`
}

func gatePartition(tier environment.Tier) records.Partition {
	return stackrecords.EdgeStacksPartition(tier)
}

func gateRecordKey(tier environment.Tier, slug string) records.Key {
	return gatePartition(tier).Key(string(albresources.Kind), gateRecordsSegment, slug)
}

func gatePublicationKey(tier environment.Tier) records.Key {
	return gatePartition(tier).Key(string(albresources.Kind), gatePublicationName)
}

func gateRecordData(tier environment.Tier, slug string) seal.AssociatedData {
	return seal.Bind("alb gate", string(tier), slug)
}

func gatePublicationData(tier environment.Tier) seal.AssociatedData {
	return seal.Bind("alb gate publication", string(tier))
}

func (e *Edge) sealRecord(ctx context.Context, tier environment.Tier, bound seal.AssociatedData, record any) (json.RawMessage, error) {
	plaintext, err := json.Marshal(record)
	if err != nil {
		return nil, fmt.Errorf("encode what the %s edge keeps about the %s gate: %w", albresources.Kind, tier, err)
	}
	sealed, err := e.deps.Cipher.Seal(ctx, tier, bound, plaintext)
	if err != nil {
		return nil, fmt.Errorf("seal what the %s edge keeps about the %s gate: %w", albresources.Kind, tier, err)
	}
	return json.Marshal(sealed)
}

func (e *Edge) openRecord(ctx context.Context, tier environment.Tier, bound seal.AssociatedData, stored json.RawMessage, into any) error {
	var sealed []byte
	if err := json.Unmarshal(stored, &sealed); err != nil {
		return fmt.Errorf("read what the %s edge keeps about the %s gate: %w", albresources.Kind, tier, err)
	}
	plaintext, err := e.deps.Cipher.Open(ctx, tier, bound, sealed)
	if err != nil {
		return fmt.Errorf("open what the %s edge keeps about the %s gate: %w", albresources.Kind, tier, err)
	}
	if err := json.Unmarshal(plaintext, into); err != nil {
		return fmt.Errorf("read what the %s edge keeps about the %s gate: %w", albresources.Kind, tier, err)
	}
	return nil
}

func (e *Edge) readGateRecord(ctx context.Context, tier environment.Tier, slug string) (gateRecord, bool, error) {
	_, record, found, err := e.readGateRecordEntry(ctx, tier, slug)
	return record, found, err
}

func (e *Edge) readGateRecordEntry(ctx context.Context, tier environment.Tier, slug string) (records.Entry, gateRecord, bool, error) {
	entry, err := records.ReadOrEmpty(ctx, e.deps.KeyValues, gateRecordKey(tier, slug))
	if err != nil {
		return records.Entry{}, gateRecord{}, false, fmt.Errorf("read the %s gate of %s on the %s edge: %w", tier, slug, albresources.Kind, err)
	}
	if len(entry.Value) == 0 {
		return entry, gateRecord{}, false, nil
	}
	var record gateRecord
	if err := e.openRecord(ctx, tier, gateRecordData(tier, slug), entry.Value, &record); err != nil {
		return records.Entry{}, gateRecord{}, false, err
	}
	return entry, record, true, nil
}

func (e *Edge) changeGateRecord(ctx context.Context, tier environment.Tier, slug string, change func(recorded gateRecord, found bool) (next gateRecord, keep bool)) (bool, error) {
	for range gateRecordAttempts {
		entry, recorded, found, err := e.readGateRecordEntry(ctx, tier, slug)
		if err != nil {
			return false, err
		}
		next, keep := change(recorded, found)
		switch {
		case !keep && !found:
			return false, nil
		case !keep:
			return true, records.Forget(ctx, e.deps.KeyValues, entry.Key)
		case found && sameGateRecord(recorded, next):
			return false, nil
		}
		if entry.Value, err = e.sealRecord(ctx, tier, gateRecordData(tier, slug), next); err != nil {
			return false, err
		}
		_, err = e.deps.KeyValues.Write(ctx, entry)
		if err == nil {
			return true, nil
		}
		if !errors.Is(err, records.ErrStale) {
			return false, fmt.Errorf("record the %s gate of %s on the %s edge: %w", tier, slug, albresources.Kind, err)
		}
	}
	return false, fmt.Errorf("record the %s gate of %s on the %s edge: it was rewritten between every read of it and the write that followed", tier, slug, albresources.Kind)
}

func sameGateRecord(a, b gateRecord) bool {
	left, _ := json.Marshal(a)
	right, _ := json.Marshal(b)
	return string(left) == string(right)
}

func (e *Edge) listGateEntries(ctx context.Context, tier environment.Tier) ([]records.Entry, error) {
	entries, err := e.deps.KeyValues.List(ctx, gatePartition(tier), string(albresources.Kind), gateRecordsSegment)
	if err != nil {
		return nil, fmt.Errorf("list the %s gates the %s edge runs: %w", tier, albresources.Kind, err)
	}
	return entries, nil
}

func (e *Edge) encodeGateConfig(ctx context.Context, tier environment.Tier) (string, error) {
	entries, err := e.listGateEntries(ctx, tier)
	if err != nil {
		return "", err
	}
	hosts := map[string]string{}
	gates := make(map[string]gate.Gate, len(entries))
	for _, entry := range entries {
		slug := entry.Key.Path[len(entry.Key.Path)-1]
		var record gateRecord
		if err := e.openRecord(ctx, tier, gateRecordData(tier, slug), entry.Value, &record); err != nil {
			return "", err
		}
		gates[slug] = record.Gate
		for _, host := range record.Hosts {
			hosts[host] = slug
		}
	}
	return gateplugin.EncodeConfig(hosts, gates), nil
}

func (e *Edge) readGatePublication(ctx context.Context, tier environment.Tier) (gatePublication, bool, error) {
	_, publication, found, err := e.readGatePublicationEntry(ctx, tier)
	return publication, found, err
}

func (e *Edge) readGatePublicationEntry(ctx context.Context, tier environment.Tier) (records.Entry, gatePublication, bool, error) {
	entry, err := records.ReadOrEmpty(ctx, e.deps.KeyValues, gatePublicationKey(tier))
	if err != nil {
		return records.Entry{}, gatePublication{}, false, fmt.Errorf("read what the gate plugin of the %s edge's %s load balancer runs: %w", albresources.Kind, tier, err)
	}
	if len(entry.Value) == 0 {
		return entry, gatePublication{}, false, nil
	}
	var publication gatePublication
	if err := e.openRecord(ctx, tier, gatePublicationData(tier), entry.Value, &publication); err != nil {
		return records.Entry{}, gatePublication{}, false, err
	}
	return entry, publication, true, nil
}

func (e *Edge) writeGatePublication(ctx context.Context, tier environment.Tier, entry records.Entry, publication gatePublication) (records.Entry, error) {
	var err error
	if entry.Value, err = e.sealRecord(ctx, tier, gatePublicationData(tier), publication); err != nil {
		return records.Entry{}, err
	}
	if entry.Revision, err = e.deps.KeyValues.Write(ctx, entry); err != nil {
		if errors.Is(err, records.ErrStale) {
			return records.Entry{}, err
		}
		return records.Entry{}, fmt.Errorf("record what the gate plugin of the %s edge's %s load balancer runs: %w", albresources.Kind, tier, err)
	}
	return entry, nil
}

func (e *Edge) forgetGates(ctx context.Context, tier environment.Tier) error {
	entries, err := e.listGateEntries(ctx, tier)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := records.Forget(ctx, e.deps.KeyValues, entry.Key); err != nil {
			return fmt.Errorf("forget the %s gate %s on the %s edge: %w", tier, entry.Key, albresources.Kind, err)
		}
	}
	return records.Forget(ctx, e.deps.KeyValues, gatePublicationKey(tier))
}

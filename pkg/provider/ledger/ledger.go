package ledger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/router"
)

const (
	casAttempts     = 8
	unpromoteWindow = 60 * time.Second
)

type Ledger struct {
	keyValues keyvalue.Store
	partition keyvalue.Partition
}

func New(store keyvalue.Store, tier environment.Tier, slug string) *Ledger {
	return &Ledger{keyValues: store, partition: Partition(tier, slug)}
}

func Partition(tier environment.Tier, slug string) keyvalue.Partition {
	partition := keyvalue.Partition{Tier: tier, Root: keyvalue.RootLedger}
	if slug != "" {
		partition.Path = []string{naming.Sanitize(slug)}
	}
	return partition
}

func RecordKey(app, build string) string { return "record:" + app + "/" + build }

func (l *Ledger) pointerKey(pointer string) keyvalue.Key {
	return l.partition.Key("pointers", pointer)
}

func (l *Ledger) deploymentKey(app, build string) keyvalue.Key {
	return l.partition.Key("records", app, build)
}

func (l *Ledger) PutStaged(ctx context.Context, record router.DeploymentRecord) error {
	if record.App == "" || record.Build == "" {
		return fmt.Errorf("stage a deployment record: it names app %q and build %q, and the ledger keys records by both", record.App, record.Build)
	}
	recorded, err := keyvalue.ReadOrEmpty(ctx, l.keyValues, l.deploymentKey(record.App, record.Build))
	if err != nil {
		return fmt.Errorf("read the deployment record for %s: %w", record.App, err)
	}
	if recorded.Value, err = json.Marshal(record); err != nil {
		return fmt.Errorf("encode the deployment record for %s: %w", record.App, err)
	}
	if _, err := l.keyValues.Write(ctx, recorded); err != nil {
		return fmt.Errorf("stage the deployment record for %s: %w", record.App, err)
	}
	return nil
}

func (l *Ledger) Record(ctx context.Context, app, build string) (router.DeploymentRecord, bool, error) {
	recorded, err := keyvalue.ReadOrEmpty(ctx, l.keyValues, l.deploymentKey(app, build))
	if err != nil {
		return router.DeploymentRecord{}, false, fmt.Errorf("read the deployment record for %s/%s: %w", app, build, err)
	}
	if len(recorded.Value) == 0 {
		return router.DeploymentRecord{}, false, nil
	}
	var record router.DeploymentRecord
	if err := json.Unmarshal(recorded.Value, &record); err != nil {
		return router.DeploymentRecord{}, false, fmt.Errorf("decode the deployment record for %s/%s: %w", app, build, err)
	}
	return record, true, nil
}

func (l *Ledger) Read(ctx context.Context, pointer string) (Pointer, error) {
	read, _, err := l.read(ctx, router.ResolvePointer(pointer))
	return read, err
}

func (l *Ledger) read(ctx context.Context, name string) (Pointer, keyvalue.Entry, error) {
	recorded, err := keyvalue.ReadOrEmpty(ctx, l.keyValues, l.pointerKey(name))
	if err != nil {
		return Pointer{}, keyvalue.Entry{}, fmt.Errorf("read the pointer %s: %w", name, err)
	}
	read := Pointer{Name: name}
	if len(recorded.Value) == 0 {
		return read, recorded, nil
	}
	if err := json.Unmarshal(recorded.Value, &read); err != nil {
		return Pointer{}, keyvalue.Entry{}, fmt.Errorf("decode the pointer %s: %w", name, err)
	}
	return read, recorded, nil
}

func (l *Ledger) change(ctx context.Context, name string, apply func(Pointer) (Pointer, error)) (Pointer, error) {
	for range casAttempts {
		current, recorded, err := l.read(ctx, name)
		if err != nil {
			return Pointer{}, err
		}
		next, err := apply(current)
		if err != nil {
			return Pointer{}, err
		}
		if recorded.Value, err = json.Marshal(next); err != nil {
			return Pointer{}, fmt.Errorf("encode the pointer %s: %w", name, err)
		}
		_, err = l.keyValues.Write(ctx, recorded)
		if errors.Is(err, keyvalue.ErrStale) {
			continue
		}
		if err != nil {
			return Pointer{}, fmt.Errorf("write the pointer %s: %w", name, err)
		}
		return next, nil
	}
	return Pointer{}, fmt.Errorf("write the pointer %s: it moved under %d attempts", name, casAttempts)
}

func (l *Ledger) Promote(ctx context.Context, promotion router.Promotion, pointer, over string) (router.PruneResult, error) {
	var dropped []Entry
	kept, err := l.change(ctx, router.ResolvePointer(pointer), func(current Pointer) (Pointer, error) {
		next, lost, err := current.Promote(promotion, over, KeptPromotions)
		dropped = lost
		return next, err
	})
	if err != nil {
		return router.PruneResult{}, err
	}
	if len(dropped) == 0 {
		return router.PruneResult{KeptPromotionIDs: collectPromotionIDs(kept.Entries)}, nil
	}
	return l.removeDropped(ctx, kept, dropped)
}

func (l *Ledger) Unpromote(ctx context.Context, promotionID, pointer string) error {
	ctx, stop := context.WithTimeout(context.WithoutCancel(ctx), unpromoteWindow)
	defer stop()
	_, err := l.change(ctx, router.ResolvePointer(pointer), func(current Pointer) (Pointer, error) {
		return current.Unpromote(promotionID)
	})
	return err
}

func (l *Ledger) History(ctx context.Context, pointer string) ([]router.HistoryEntry, error) {
	read, err := l.Read(ctx, pointer)
	if err != nil {
		return nil, err
	}
	return read.History(), nil
}

func (l *Ledger) ActivePromotionID(ctx context.Context, pointer string) (string, error) {
	read, err := l.Read(ctx, pointer)
	return read.Active, err
}

func (l *Ledger) ReadActive(ctx context.Context, pointer string) (router.Promotion, bool, error) {
	read, err := l.Read(ctx, pointer)
	if err != nil {
		return router.Promotion{}, false, err
	}
	at := read.findEntry(read.Active)
	if at < 0 {
		return router.Promotion{}, false, nil
	}
	return read.Entries[at].Promotion, true, nil
}

func (l *Ledger) Prune(ctx context.Context, keepN int, pointer string) (router.PruneResult, error) {
	var dropped []Entry
	kept, err := l.change(ctx, router.ResolvePointer(pointer), func(current Pointer) (Pointer, error) {
		next, lost := current.Retain(keepN)
		dropped = lost
		return next, nil
	})
	if err != nil {
		return router.PruneResult{}, err
	}
	return l.removeDropped(ctx, kept, dropped)
}

func (l *Ledger) RemovePointer(ctx context.Context, pointer string) (router.PruneResult, error) {
	name := router.ResolvePointer(pointer)
	for range casAttempts {
		current, recorded, err := l.read(ctx, name)
		if err != nil {
			return router.PruneResult{}, err
		}
		if len(recorded.Value) > 0 {
			err = l.keyValues.Remove(ctx, recorded.Key, recorded.Revision)
		}
		if errors.Is(err, keyvalue.ErrStale) {
			continue
		}
		if err != nil && !errors.Is(err, keyvalue.ErrNotFound) {
			return router.PruneResult{}, fmt.Errorf("remove the pointer %s: %w", name, err)
		}
		return l.removeDropped(ctx, Pointer{Name: name}, current.Entries)
	}
	return router.PruneResult{}, fmt.Errorf("remove the pointer %s: it moved under %d attempts", name, casAttempts)
}

func (l *Ledger) removeDropped(ctx context.Context, kept Pointer, dropped []Entry) (router.PruneResult, error) {
	named, err := l.readRecordKeysNamedElsewhere(ctx, kept.Name)
	if err != nil {
		return router.PruneResult{}, err
	}
	keptKeys := collectRecordKeys(kept.Entries)
	var removed []string
	for _, entry := range dropped {
		for app, build := range entry.Builds {
			key := RecordKey(app, build)
			if slices.Contains(keptKeys, key) || named[key] || slices.Contains(removed, key) {
				continue
			}
			if err := keyvalue.Forget(ctx, l.keyValues, l.deploymentKey(app, build)); err != nil {
				return router.PruneResult{}, fmt.Errorf("remove the deployment record %s/%s no promotion names: %w", app, build, err)
			}
			removed = append(removed, key)
		}
	}
	slices.Sort(removed)
	surviving, err := l.readRecordKeys(ctx)
	if err != nil {
		return router.PruneResult{}, err
	}
	return router.PruneResult{
		KeptPromotionIDs:           collectPromotionIDs(kept.Entries),
		RemovedPromotionIDs:        collectPromotionIDs(dropped),
		RemovedRecordKeys:          removed,
		SurvivingRecordKeys:        surviving,
		SurvivingPointerRecordKeys: keptKeys,
	}, nil
}

func (l *Ledger) readRecordKeysNamedElsewhere(ctx context.Context, name string) (map[string]bool, error) {
	recorded, err := l.keyValues.List(ctx, l.partition, "pointers")
	if err != nil {
		return nil, fmt.Errorf("read the pointers for %s: %w", l.partition, err)
	}
	named := map[string]bool{}
	for _, entry := range recorded {
		var other Pointer
		if err := json.Unmarshal(entry.Value, &other); err != nil {
			return nil, fmt.Errorf("decode the pointer %s: %w", entry.Key, err)
		}
		if other.Name == name {
			continue
		}
		for _, key := range collectRecordKeys(other.Entries) {
			named[key] = true
		}
	}
	return named, nil
}

func (l *Ledger) Pointers(ctx context.Context) ([]string, error) {
	recorded, err := l.keyValues.List(ctx, l.partition, "pointers")
	if err != nil {
		return nil, fmt.Errorf("read the pointers for %s: %w", l.partition, err)
	}
	names := make([]string, 0, len(recorded))
	for _, entry := range recorded {
		rest, under := entry.Key.Under("pointers")
		if !under || len(rest) != 1 {
			continue
		}
		names = append(names, rest[0])
	}
	slices.Sort(names)
	return names, nil
}

func (l *Ledger) Destroy(ctx context.Context) error {
	recorded, err := l.keyValues.List(ctx, l.partition)
	if err != nil {
		return fmt.Errorf("read the deployments ledger for %s: %w", l.partition, err)
	}
	for _, entry := range recorded {
		if err := keyvalue.Forget(ctx, l.keyValues, entry.Key); err != nil {
			return fmt.Errorf("erase the deployments ledger for %s: %w", l.partition, err)
		}
	}
	return nil
}

func (l *Ledger) readRecordKeys(ctx context.Context) ([]string, error) {
	recorded, err := l.keyValues.List(ctx, l.partition, "records")
	if err != nil {
		return nil, fmt.Errorf("read the deployment records for %s: %w", l.partition, err)
	}
	keys := make([]string, 0, len(recorded))
	for _, entry := range recorded {
		rest, under := entry.Key.Under("records")
		if !under || len(rest) != 2 {
			continue
		}
		keys = append(keys, RecordKey(rest[0], rest[1]))
	}
	slices.Sort(keys)
	return keys, nil
}

func collectPromotionIDs(entries []Entry) []string {
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		ids = append(ids, entry.PromotionID)
	}
	return ids
}

func collectRecordKeys(entries []Entry) []string {
	var keys []string
	for _, entry := range entries {
		for app, build := range entry.Builds {
			if key := RecordKey(app, build); !slices.Contains(keys, key) {
				keys = append(keys, key)
			}
		}
	}
	slices.Sort(keys)
	return keys
}

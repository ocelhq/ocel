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
	stored, err := keyvalue.ReadOrEmpty(ctx, l.keyValues, l.deploymentKey(record.App, record.Build))
	if err != nil {
		return fmt.Errorf("read the deployment record for %s: %w", record.App, err)
	}
	if stored.Value, err = json.Marshal(record); err != nil {
		return fmt.Errorf("encode the deployment record for %s: %w", record.App, err)
	}
	if _, err := l.keyValues.Write(ctx, stored); err != nil {
		return fmt.Errorf("stage the deployment record for %s: %w", record.App, err)
	}
	return nil
}

func (l *Ledger) Record(ctx context.Context, app, build string) (router.DeploymentRecord, bool, error) {
	stored, err := keyvalue.ReadOrEmpty(ctx, l.keyValues, l.deploymentKey(app, build))
	if err != nil {
		return router.DeploymentRecord{}, false, fmt.Errorf("read the deployment record for %s/%s: %w", app, build, err)
	}
	if len(stored.Value) == 0 {
		return router.DeploymentRecord{}, false, nil
	}
	var record router.DeploymentRecord
	if err := json.Unmarshal(stored.Value, &record); err != nil {
		return router.DeploymentRecord{}, false, fmt.Errorf("decode the deployment record for %s/%s: %w", app, build, err)
	}
	return record, true, nil
}

func (l *Ledger) Read(ctx context.Context, pointer string) (Pointer, error) {
	read, _, err := l.read(ctx, router.ResolvePointer(pointer))
	return read, err
}

func (l *Ledger) read(ctx context.Context, name string) (Pointer, keyvalue.Entry, error) {
	stored, err := keyvalue.ReadOrEmpty(ctx, l.keyValues, l.pointerKey(name))
	if err != nil {
		return Pointer{}, keyvalue.Entry{}, fmt.Errorf("read the pointer %s: %w", name, err)
	}
	read := Pointer{Name: name}
	if len(stored.Value) == 0 {
		return read, stored, nil
	}
	if err := json.Unmarshal(stored.Value, &read); err != nil {
		return Pointer{}, keyvalue.Entry{}, fmt.Errorf("decode the pointer %s: %w", name, err)
	}
	return read, stored, nil
}

func (l *Ledger) change(ctx context.Context, name string, apply func(Pointer) (Pointer, []RecordedPromotion, error)) (Pointer, []RecordedPromotion, error) {
	for range casAttempts {
		current, stored, err := l.read(ctx, name)
		if err != nil {
			return Pointer{}, nil, err
		}
		next, dropped, err := apply(current)
		if err != nil {
			return Pointer{}, nil, err
		}
		err = l.write(ctx, stored, next)
		if errors.Is(err, keyvalue.ErrStale) {
			continue
		}
		if err != nil {
			return Pointer{}, nil, err
		}
		return next, dropped, nil
	}
	return Pointer{}, nil, fmt.Errorf("write the pointer %s: it moved under %d attempts", name, casAttempts)
}

func (l *Ledger) write(ctx context.Context, stored keyvalue.Entry, next Pointer) error {
	if next.Active == "" && len(next.Promotions) == 0 {
		if len(stored.Value) == 0 {
			return nil
		}
		err := l.keyValues.Remove(ctx, stored.Key, stored.Revision)
		if errors.Is(err, keyvalue.ErrNotFound) {
			return keyvalue.ErrStale
		}
		if err != nil && !errors.Is(err, keyvalue.ErrStale) {
			return fmt.Errorf("remove the pointer %s: %w", next.Name, err)
		}
		return err
	}
	var err error
	if stored.Value, err = json.Marshal(next); err != nil {
		return fmt.Errorf("encode the pointer %s: %w", next.Name, err)
	}
	_, err = l.keyValues.Write(ctx, stored)
	if err != nil && !errors.Is(err, keyvalue.ErrStale) {
		return fmt.Errorf("write the pointer %s: %w", next.Name, err)
	}
	return err
}

func (l *Ledger) Promote(ctx context.Context, promotion router.Promotion, pointer, replaces string) (router.PruneResult, error) {
	kept, dropped, err := l.change(ctx, router.ResolvePointer(pointer), func(current Pointer) (Pointer, []RecordedPromotion, error) {
		return current.Promote(promotion, replaces, KeptPromotions)
	})
	if err != nil {
		return router.PruneResult{}, err
	}
	if len(dropped) == 0 {
		return router.PruneResult{KeptPromotionIDs: collectPromotionIDs(kept.Promotions)}, nil
	}
	return l.removeDropped(ctx, kept, dropped)
}

func (l *Ledger) changeDropping(ctx context.Context, pointer string, apply func(Pointer) (Pointer, []RecordedPromotion, error)) (router.PruneResult, error) {
	kept, dropped, err := l.change(ctx, router.ResolvePointer(pointer), apply)
	if err != nil {
		return router.PruneResult{}, err
	}
	return l.removeDropped(ctx, kept, dropped)
}

func (l *Ledger) Unpromote(ctx context.Context, promotionID, pointer string) error {
	ctx, stop := context.WithTimeout(context.WithoutCancel(ctx), unpromoteWindow)
	defer stop()
	_, _, err := l.change(ctx, router.ResolvePointer(pointer), func(current Pointer) (Pointer, []RecordedPromotion, error) {
		next, err := current.Unpromote(promotionID)
		return next, nil, err
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
	at := read.findPromotion(read.Active)
	if at < 0 {
		return router.Promotion{}, false, nil
	}
	return read.Promotions[at].Promotion, true, nil
}

func (l *Ledger) Prune(ctx context.Context, keep int, pointer string) (router.PruneResult, error) {
	return l.changeDropping(ctx, pointer, func(current Pointer) (Pointer, []RecordedPromotion, error) {
		kept, dropped := current.Retain(keep)
		return kept, dropped, nil
	})
}

func (l *Ledger) RemovePointer(ctx context.Context, pointer string) (router.PruneResult, error) {
	return l.changeDropping(ctx, pointer, func(current Pointer) (Pointer, []RecordedPromotion, error) {
		return Pointer{Name: current.Name}, current.Promotions, nil
	})
}

func (l *Ledger) removeDropped(ctx context.Context, kept Pointer, dropped []RecordedPromotion) (router.PruneResult, error) {
	named, err := l.readRecordKeysNamedElsewhere(ctx, kept.Name)
	if err != nil {
		return router.PruneResult{}, err
	}
	keptKeys := collectRecordKeys(kept.Promotions)
	var removed []string
	for _, recorded := range dropped {
		for app, build := range recorded.Builds {
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
		KeptPromotionIDs:           collectPromotionIDs(kept.Promotions),
		RemovedPromotionIDs:        collectPromotionIDs(dropped),
		RemovedRecordKeys:          removed,
		SurvivingRecordKeys:        surviving,
		SurvivingPointerRecordKeys: keptKeys,
	}, nil
}

func (l *Ledger) readRecordKeysNamedElsewhere(ctx context.Context, name string) (map[string]bool, error) {
	stored, err := l.keyValues.List(ctx, l.partition, "pointers")
	if err != nil {
		return nil, fmt.Errorf("read the pointers for %s: %w", l.partition, err)
	}
	named := map[string]bool{}
	for _, entry := range stored {
		var other Pointer
		if err := json.Unmarshal(entry.Value, &other); err != nil {
			return nil, fmt.Errorf("decode the pointer %s: %w", entry.Key, err)
		}
		if other.Name == name {
			continue
		}
		for _, key := range collectRecordKeys(other.Promotions) {
			named[key] = true
		}
	}
	return named, nil
}

func (l *Ledger) Pointers(ctx context.Context) ([]string, error) {
	stored, err := l.keyValues.List(ctx, l.partition, "pointers")
	if err != nil {
		return nil, fmt.Errorf("read the pointers for %s: %w", l.partition, err)
	}
	names := make([]string, 0, len(stored))
	for _, entry := range stored {
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
	stored, err := l.keyValues.List(ctx, l.partition)
	if err != nil {
		return fmt.Errorf("read the deployments ledger for %s: %w", l.partition, err)
	}
	for _, entry := range stored {
		if err := keyvalue.Forget(ctx, l.keyValues, entry.Key); err != nil {
			return fmt.Errorf("erase the deployments ledger for %s: %w", l.partition, err)
		}
	}
	return nil
}

func (l *Ledger) readRecordKeys(ctx context.Context) ([]string, error) {
	stored, err := l.keyValues.List(ctx, l.partition, "records")
	if err != nil {
		return nil, fmt.Errorf("read the deployment records for %s: %w", l.partition, err)
	}
	keys := make([]string, 0, len(stored))
	for _, entry := range stored {
		rest, under := entry.Key.Under("records")
		if !under || len(rest) != 2 {
			continue
		}
		keys = append(keys, RecordKey(rest[0], rest[1]))
	}
	slices.Sort(keys)
	return keys, nil
}

func collectPromotionIDs(promotions []RecordedPromotion) []string {
	ids := make([]string, 0, len(promotions))
	for _, recorded := range promotions {
		ids = append(ids, recorded.PromotionID)
	}
	return ids
}

func collectRecordKeys(promotions []RecordedPromotion) []string {
	var keys []string
	for _, recorded := range promotions {
		for app, build := range recorded.Builds {
			if key := RecordKey(app, build); !slices.Contains(keys, key) {
				keys = append(keys, key)
			}
		}
	}
	slices.Sort(keys)
	return keys
}

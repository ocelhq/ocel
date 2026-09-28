package ledger

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
)

const (
	casAttempts  = 8
	unwindWindow = 60 * time.Second
)

func detachedForUnwind(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), unwindWindow)
}

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

func (l *Ledger) key(path ...string) keyvalue.Key { return l.partition.Key(path...) }

func (l *Ledger) sequenceKey() keyvalue.Key { return l.key("seq") }

func (l *Ledger) pointerKey(pointer string) keyvalue.Key {
	return l.key("pointers", pointer)
}

func (l *Ledger) promotionKey(pointer, id string) keyvalue.Key {
	return l.key("promotions", pointer, id)
}

func (l *Ledger) deploymentKey(app, build string) keyvalue.Key {
	return l.key("records", app, build)
}

func (l *Ledger) tagKey(tag string) keyvalue.Key { return l.key("tags", tag) }

func pointerOr(pointer string) string {
	if pointer == "" {
		return router.DefaultPointer
	}
	return pointer
}

const displacedDepth = 16

type promotionRecord struct {
	router.Promotion
	Seq     int64          `json:"seq"`
	Claimed bool           `json:"claimed,omitempty"`
	Prior   *priorPosition `json:"was,omitempty"`
}

type priorPosition struct {
	Seq int64 `json:"seq"`
	Ts  int64 `json:"ts"`
}

type pointerRecord struct {
	PromotionID string   `json:"promotionId"`
	Displaced   []string `json:"displaced,omitempty"`
}

func (p pointerRecord) movedTo(promotionID string) pointerRecord {
	if p.PromotionID == "" {
		return pointerRecord{PromotionID: promotionID}
	}
	displaced := append([]string{p.PromotionID}, p.Displaced...)
	return pointerRecord{PromotionID: promotionID, Displaced: displaced[:min(len(displaced), displacedDepth)]}
}

func (p pointerRecord) without(promotionID string) (pointerRecord, bool) {
	if p.PromotionID == promotionID {
		if len(p.Displaced) == 0 {
			return pointerRecord{}, true
		}
		return pointerRecord{PromotionID: p.Displaced[0], Displaced: p.Displaced[1:]}, true
	}
	at := slices.Index(p.Displaced, promotionID)
	if at < 0 {
		return p, false
	}
	return pointerRecord{PromotionID: p.PromotionID, Displaced: slices.Delete(slices.Clone(p.Displaced), at, at+1)}, true
}

type tagRecord struct {
	PromotionID string `json:"promotionId"`
}

func (l *Ledger) PutStaged(ctx context.Context, record router.DeploymentRecord) error {
	if record.App == "" || record.Build == "" {
		return fmt.Errorf("stage a deployment record: it names app %q and build %q, and the ledger keys records by both", record.App, record.Build)
	}
	recorded, err := keyvalue.ReadOrEmpty(ctx, l.keyValues, l.deploymentKey(record.App, record.Build))
	if err != nil {
		return fmt.Errorf("read the deployment record for %s: %w", record.App, err)
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode the deployment record for %s: %w", record.App, err)
	}
	recorded.Value = encoded
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

func (l *Ledger) Promote(ctx context.Context, promotion router.Promotion, pointer string, _ progress.Progress) error {
	name := pointerOr(pointer)
	claimed, err := l.claimTag(ctx, promotion)
	if err != nil {
		return err
	}
	seq, err := l.nextSequence(ctx)
	if err != nil {
		return err
	}
	at, err := keyvalue.ReadOrEmpty(ctx, l.keyValues, l.pointerKey(name))
	if err != nil {
		return fmt.Errorf("read what %s points at: %w", name, err)
	}
	current, err := pointerOf(name, at)
	if err != nil {
		return err
	}
	recorded, err := keyvalue.ReadOrEmpty(ctx, l.keyValues, l.promotionKey(name, promotion.PromotionID))
	if err != nil {
		return fmt.Errorf("read promotion %s: %w", promotion.PromotionID, err)
	}
	row := promotionRecord{Promotion: promotion, Seq: seq, Claimed: claimed}
	if len(recorded.Value) > 0 {
		var prior promotionRecord
		if err := json.Unmarshal(recorded.Value, &prior); err != nil {
			return fmt.Errorf("decode promotion %s: %w", promotion.PromotionID, err)
		}
		row.Prior = &priorPosition{Seq: prior.Seq, Ts: prior.Ts}
	}
	encoded, err := json.Marshal(row)
	if err != nil {
		return fmt.Errorf("encode promotion %s: %w", promotion.PromotionID, err)
	}
	recorded.Value = encoded
	if _, err := l.keyValues.Write(ctx, recorded); err != nil {
		return fmt.Errorf("record promotion %s: %w", promotion.PromotionID, err)
	}

	flipped, err := json.Marshal(current.movedTo(promotion.PromotionID))
	if err != nil {
		return fmt.Errorf("encode the pointer %s: %w", name, err)
	}
	at.Value = flipped
	if _, err := l.keyValues.Write(ctx, at); err != nil {
		if errors.Is(err, keyvalue.ErrStale) {
			activeID, readErr := l.pointerAt(ctx, name)
			if readErr != nil || activeID == "" {
				activeID = "another promotion"
			}
			busy := refusal.Refuse(refusal.CodeBusy,
				"point %s at promotion %s: %s now points at %s, so another deploy moved it while this one was promoting, and this deploy stopped rather than overwrite it. The release it staged is still recorded: reach it with `ocel rollback --to %s`, or re-run this deploy once the other one has finished",
				name, promotion.PromotionID, activeID, name, promotion.PromotionID)
			if err := l.retract(ctx, name, promotion.PromotionID); err != nil {
				return errors.Join(busy, err)
			}
			return busy
		}
		return fmt.Errorf("point %s at promotion %s: %w", name, promotion.PromotionID, err)
	}
	return nil
}

func (l *Ledger) Unpromote(ctx context.Context, promotionID, pointer string) error {
	ctx, stop := detachedForUnwind(ctx)
	defer stop()
	name := pointerOr(pointer)
	for range casAttempts {
		at, err := keyvalue.ReadOrEmpty(ctx, l.keyValues, l.pointerKey(name))
		if err != nil {
			return fmt.Errorf("read what %s points at: %w", name, err)
		}
		current, err := pointerOf(name, at)
		if err != nil {
			return err
		}
		back, found := current.without(promotionID)
		if !found {
			return l.retract(ctx, name, promotionID)
		}
		if back.PromotionID == "" {
			err = l.keyValues.Remove(ctx, at.Key, at.Revision)
		} else {
			at.Value, err = json.Marshal(back)
			if err == nil {
				_, err = l.keyValues.Write(ctx, at)
			}
		}
		if errors.Is(err, keyvalue.ErrStale) {
			continue
		}
		if err != nil {
			return fmt.Errorf("point %s back from promotion %s: %w", name, promotionID, err)
		}
		return l.retract(ctx, name, promotionID)
	}
	return fmt.Errorf("point %s back from promotion %s: it moved under %d attempts", name, promotionID, casAttempts)
}

func (l *Ledger) retract(ctx context.Context, pointer, promotionID string) error {
	ctx, stop := detachedForUnwind(ctx)
	defer stop()
	for range casAttempts {
		recorded, err := keyvalue.ReadOrEmpty(ctx, l.keyValues, l.promotionKey(pointer, promotionID))
		if err != nil {
			return fmt.Errorf("read promotion %s: %w", promotionID, err)
		}
		if len(recorded.Value) == 0 {
			return fmt.Errorf("take back promotion %s: %s records no such promotion", promotionID, pointer)
		}
		var row promotionRecord
		if err := json.Unmarshal(recorded.Value, &row); err != nil {
			return fmt.Errorf("decode promotion %s: %w", promotionID, err)
		}
		if !row.Claimed && row.Prior == nil {
			return nil
		}
		freed := ""
		if row.Claimed {
			freed, row.Tag, row.Claimed = row.Tag, "", false
		}
		if row.Prior != nil {
			row.Seq, row.Ts, row.Prior = row.Prior.Seq, row.Prior.Ts, nil
		}
		if recorded.Value, err = json.Marshal(row); err != nil {
			return fmt.Errorf("encode promotion %s: %w", promotionID, err)
		}
		if _, err := l.keyValues.Write(ctx, recorded); err != nil {
			if errors.Is(err, keyvalue.ErrStale) {
				continue
			}
			return fmt.Errorf("take back promotion %s: %w", promotionID, err)
		}
		return l.freeTag(ctx, freed, promotionID)
	}
	return fmt.Errorf("take back promotion %s: it moved under %d attempts", promotionID, casAttempts)
}

func (l *Ledger) freeTag(ctx context.Context, tag, promotionID string) error {
	if tag == "" {
		return nil
	}
	for range casAttempts {
		recorded, err := keyvalue.ReadOrEmpty(ctx, l.keyValues, l.tagKey(tag))
		if err != nil {
			return fmt.Errorf("read the tag %q: %w", tag, err)
		}
		if len(recorded.Value) == 0 {
			return nil
		}
		var claimed tagRecord
		if err := json.Unmarshal(recorded.Value, &claimed); err != nil {
			return fmt.Errorf("decode the tag %q: %w", tag, err)
		}
		if claimed.PromotionID != promotionID {
			return nil
		}
		err = l.keyValues.Remove(ctx, recorded.Key, recorded.Revision)
		if errors.Is(err, keyvalue.ErrStale) {
			continue
		}
		if err != nil && !errors.Is(err, keyvalue.ErrNotFound) {
			return fmt.Errorf("free the tag %q: %w", tag, err)
		}
		return nil
	}
	return fmt.Errorf("free the tag %q: it moved under %d attempts", tag, casAttempts)
}

func (l *Ledger) claimTag(ctx context.Context, promotion router.Promotion) (bool, error) {
	if promotion.Tag == "" {
		return false, nil
	}
	recorded, err := keyvalue.ReadOrEmpty(ctx, l.keyValues, l.tagKey(promotion.Tag))
	if err != nil {
		return false, fmt.Errorf("read the tag %q: %w", promotion.Tag, err)
	}
	if len(recorded.Value) > 0 {
		var claimed tagRecord
		if err := json.Unmarshal(recorded.Value, &claimed); err != nil {
			return false, fmt.Errorf("decode the tag %q: %w", promotion.Tag, err)
		}
		if claimed.PromotionID == promotion.PromotionID {
			return false, nil
		}
		return false, l.tagTaken(promotion, claimed.PromotionID)
	}
	encoded, err := json.Marshal(tagRecord{PromotionID: promotion.PromotionID})
	if err != nil {
		return false, fmt.Errorf("encode the tag %q: %w", promotion.Tag, err)
	}
	if _, err := l.keyValues.Write(ctx, keyvalue.Entry{Key: l.tagKey(promotion.Tag), Value: encoded}); err != nil {
		if errors.Is(err, keyvalue.ErrStale) {
			return false, l.tagTaken(promotion, l.tagOwner(ctx, promotion.Tag))
		}
		return false, fmt.Errorf("claim the tag %q: %w", promotion.Tag, err)
	}
	return true, nil
}

func (l *Ledger) tagOwner(ctx context.Context, tag string) string {
	recorded, err := keyvalue.ReadOrEmpty(ctx, l.keyValues, l.tagKey(tag))
	if err != nil || len(recorded.Value) == 0 {
		return ""
	}
	var claimed tagRecord
	if err := json.Unmarshal(recorded.Value, &claimed); err != nil {
		return ""
	}
	return claimed.PromotionID
}

func (l *Ledger) tagTaken(promotion router.Promotion, owner string) error {
	if owner == "" {
		owner = "another promotion"
	}
	return refusal.Refuse(refusal.CodeInvalid,
		"promote %s: the tag %q already names promotion %s in this project, and a tag names one release so that `ocel rollback --tag %s` is unambiguous. Pick another tag, or roll back to %s instead",
		promotion.PromotionID, promotion.Tag, owner, promotion.Tag, owner)
}

func (l *Ledger) nextSequence(ctx context.Context) (int64, error) {
	for range casAttempts {
		recorded, err := keyvalue.ReadOrEmpty(ctx, l.keyValues, l.sequenceKey())
		if err != nil {
			return 0, fmt.Errorf("read the promotion sequence for %s: %w", l.partition, err)
		}
		next := int64(1)
		if len(recorded.Value) > 0 {
			var current int64
			if err := json.Unmarshal(recorded.Value, &current); err != nil {
				return 0, fmt.Errorf("read the promotion sequence for %s: %w", l.partition, err)
			}
			next = current + 1
		}
		if recorded.Value, err = json.Marshal(next); err != nil {
			return 0, fmt.Errorf("encode the promotion sequence for %s: %w", l.partition, err)
		}
		if _, err := l.keyValues.Write(ctx, recorded); err != nil {
			if errors.Is(err, keyvalue.ErrStale) {
				continue
			}
			return 0, fmt.Errorf("advance the promotion sequence for %s: %w", l.partition, err)
		}
		return next, nil
	}
	return 0, fmt.Errorf("advance the promotion sequence for %s: it moved under %d attempts", l.partition, casAttempts)
}

func (l *Ledger) History(ctx context.Context, pointer string) ([]router.HistoryEntry, error) {
	name := pointerOr(pointer)
	rows, err := l.promotions(ctx, name)
	if err != nil {
		return nil, err
	}
	active, err := l.pointerAt(ctx, name)
	if err != nil {
		return nil, err
	}
	entries := make([]router.HistoryEntry, 0, len(rows))
	for _, row := range rows {
		entries = append(entries, router.HistoryEntry{Promotion: row.Promotion, Active: row.PromotionID == active})
	}
	return entries, nil
}

func (l *Ledger) Prune(ctx context.Context, keepN int, pointer string) (router.PruneResult, error) {
	name := pointerOr(pointer)
	rows, err := l.promotions(ctx, name)
	if err != nil {
		return router.PruneResult{}, err
	}
	active, err := l.pointerAt(ctx, name)
	if err != nil {
		return router.PruneResult{}, err
	}
	kept, removed := Retain(rows, keepN, active, func(row promotionRecord) string { return row.PromotionID })
	keptKeys := recordKeysOf(kept)
	if err := l.deletePromotions(ctx, name, removed, keptKeys); err != nil {
		return router.PruneResult{}, err
	}
	surviving, err := l.recordKeys(ctx)
	if err != nil {
		return router.PruneResult{}, err
	}
	return router.PruneResult{
		KeptPromotionIDs:           promotionIDs(kept),
		RemovedPromotionIDs:        promotionIDs(removed),
		RemovedRecordKeys:          without(recordKeysOf(removed), keptKeys),
		SurvivingRecordKeys:        surviving,
		SurvivingPointerRecordKeys: keptKeys,
	}, nil
}

func Retain[T any](rows []T, keepN int, active string, id func(T) string) (kept, removed []T) {
	for i, row := range rows {
		if i < keepN || id(row) == active {
			kept = append(kept, row)
			continue
		}
		removed = append(removed, row)
	}
	return kept, removed
}

func (l *Ledger) RemovePointer(ctx context.Context, pointer string) (router.PruneResult, error) {
	name := pointerOr(pointer)
	rows, err := l.promotions(ctx, name)
	if err != nil {
		return router.PruneResult{}, err
	}
	if err := l.deletePromotions(ctx, name, rows, nil); err != nil {
		return router.PruneResult{}, err
	}
	if err := keyvalue.Forget(ctx, l.keyValues, l.pointerKey(name)); err != nil {
		return router.PruneResult{}, fmt.Errorf("forget pointer %s: %w", name, err)
	}
	surviving, err := l.recordKeys(ctx)
	if err != nil {
		return router.PruneResult{}, err
	}
	return router.PruneResult{
		RemovedPromotionIDs: promotionIDs(rows),
		RemovedRecordKeys:   recordKeysOf(rows),
		SurvivingRecordKeys: surviving,
	}, nil
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

func (l *Ledger) deletePromotions(ctx context.Context, pointer string, rows []promotionRecord, keptKeys []string) error {
	for _, row := range rows {
		names := []keyvalue.Key{l.promotionKey(pointer, row.PromotionID)}
		for app, build := range row.Builds {
			if slices.Contains(keptKeys, RecordKey(app, build)) {
				continue
			}
			names = append(names, l.deploymentKey(app, build))
		}
		if row.Tag != "" {
			names = append(names, l.tagKey(row.Tag))
		}
		for _, name := range names {
			if err := keyvalue.Forget(ctx, l.keyValues, name); err != nil {
				return fmt.Errorf("drop the promotions %s no longer keeps: %w", pointer, err)
			}
		}
	}
	return nil
}

func (l *Ledger) ActivePromotionID(ctx context.Context, pointer string) (string, error) {
	return l.pointerAt(ctx, pointerOr(pointer))
}

func (l *Ledger) pointerAt(ctx context.Context, pointer string) (string, error) {
	recorded, err := keyvalue.ReadOrEmpty(ctx, l.keyValues, l.pointerKey(pointer))
	if err != nil {
		return "", fmt.Errorf("read what %s points at: %w", pointer, err)
	}
	at, err := pointerOf(pointer, recorded)
	return at.PromotionID, err
}

func pointerOf(pointer string, recorded keyvalue.Entry) (pointerRecord, error) {
	if len(recorded.Value) == 0 {
		return pointerRecord{}, nil
	}
	var at pointerRecord
	if err := json.Unmarshal(recorded.Value, &at); err != nil {
		return pointerRecord{}, fmt.Errorf("decode what %s points at: %w", pointer, err)
	}
	return at, nil
}

func (l *Ledger) promotions(ctx context.Context, pointer string) ([]promotionRecord, error) {
	recorded, err := l.keyValues.List(ctx, l.partition, "promotions", pointer)
	if err != nil {
		return nil, fmt.Errorf("read the promotions for %s: %w", pointer, err)
	}
	rows := make([]promotionRecord, 0, len(recorded))
	for _, entry := range recorded {
		var row promotionRecord
		if err := json.Unmarshal(entry.Value, &row); err != nil {
			return nil, fmt.Errorf("decode promotion %s: %w", entry.Key, err)
		}
		rows = append(rows, row)
	}
	slices.SortFunc(rows, func(a, b promotionRecord) int {
		if n := cmp.Compare(b.Seq, a.Seq); n != 0 {
			return n
		}
		return cmp.Compare(b.PromotionID, a.PromotionID)
	})
	return rows, nil
}

func (l *Ledger) recordKeys(ctx context.Context) ([]string, error) {
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

func promotionIDs(rows []promotionRecord) []string {
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.PromotionID)
	}
	return ids
}

func without(keys, kept []string) []string {
	return slices.DeleteFunc(slices.Clone(keys), func(key string) bool { return slices.Contains(kept, key) })
}

func recordKeysOf(rows []promotionRecord) []string {
	seen := map[string]bool{}
	var keys []string
	for _, row := range rows {
		for app, build := range row.Builds {
			k := RecordKey(app, build)
			if seen[k] {
				continue
			}
			seen[k] = true
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	return keys
}

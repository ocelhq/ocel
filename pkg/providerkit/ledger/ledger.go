package ledger

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit/records"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	casAttempts  = 8
	unwindWindow = 60 * time.Second
)

func detachedForUnwind(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), unwindWindow)
}

type Ledger struct {
	records records.Store
	scope   string
}

var _ edge.Ledger = (*Ledger)(nil)

func New(records records.Store, class edge.Class, slug string) *Ledger {
	return &Ledger{records: records, scope: Scope(class, slug)}
}

func Scope(class edge.Class, slug string) string {
	if slug == "" {
		return string(class)
	}
	return string(class) + naming.PathSeparator + naming.Sanitize(slug)
}

func RecordKey(app, build string) string { return "record:" + app + "/" + build }

func (l *Ledger) name(rest ...string) records.Name {
	return append(records.Name{records.RootLedger, l.scope}, rest...)
}

func (l *Ledger) schemaName() records.Name { return l.name("schema") }

func (l *Ledger) sequenceName() records.Name { return l.name("seq") }

func (l *Ledger) pointersName() records.Name { return l.name("pointers") }

func (l *Ledger) pointerName(pointer string) records.Name {
	return l.name("pointers", pointer)
}

func (l *Ledger) promotionsName(pointer string) records.Name {
	return l.name("promotions", pointer)
}

func (l *Ledger) promotionName(pointer, id string) records.Name {
	return l.name("promotions", pointer, id)
}

func (l *Ledger) recordsName() records.Name { return l.name("records") }

func (l *Ledger) recordName(app, build string) records.Name {
	return l.name("records", app, build)
}

func (l *Ledger) tagName(tag string) records.Name { return l.name("tags", tag) }

func pointerOr(pointer string) string {
	if pointer == "" {
		return edge.DefaultPointer
	}
	return pointer
}

const displacedDepth = 16

type promotionRecord struct {
	edge.Promotion
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

func (l *Ledger) EnsureSchema(ctx context.Context) error {
	held, err := records.ReadOrEmpty(ctx, l.records, l.schemaName())
	if err != nil {
		return fmt.Errorf("read the deployments ledger schema for %s: %w", l.scope, err)
	}
	held.Bytes = []byte(strconv.Itoa(edge.StoreSchemaVersion))
	if _, err := l.records.Write(ctx, held); err != nil {
		return fmt.Errorf("record the deployments ledger schema for %s: %w", l.scope, err)
	}
	return nil
}

func (l *Ledger) SchemaVersion(ctx context.Context) (int, error) {
	held, err := records.ReadOrEmpty(ctx, l.records, l.schemaName())
	if err != nil {
		return 0, fmt.Errorf("read the deployments ledger schema for %s: %w", l.scope, err)
	}
	if len(held.Bytes) == 0 {
		return 0, edge.ErrStoreSchemaUnreadable
	}
	version, err := strconv.Atoi(string(held.Bytes))
	if err != nil {
		return 0, fmt.Errorf("read the deployments ledger schema for %s: %w", l.scope, err)
	}
	return version, nil
}

func (l *Ledger) PutStaged(ctx context.Context, record edge.DeploymentRecord) error {
	if record.App == "" || record.Build == "" {
		return fmt.Errorf("stage a deployment record: it names app %q and build %q, and the ledger keys records by both", record.App, record.Build)
	}
	held, err := records.ReadOrEmpty(ctx, l.records, l.recordName(record.App, record.Build))
	if err != nil {
		return fmt.Errorf("read the deployment record for %s: %w", record.App, err)
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode the deployment record for %s: %w", record.App, err)
	}
	held.Bytes = encoded
	if _, err := l.records.Write(ctx, held); err != nil {
		return fmt.Errorf("stage the deployment record for %s: %w", record.App, err)
	}
	return nil
}

func (l *Ledger) Record(ctx context.Context, app, build string) (edge.DeploymentRecord, bool, error) {
	held, err := records.ReadOrEmpty(ctx, l.records, l.recordName(app, build))
	if err != nil {
		return edge.DeploymentRecord{}, false, fmt.Errorf("read the deployment record for %s/%s: %w", app, build, err)
	}
	if len(held.Bytes) == 0 {
		return edge.DeploymentRecord{}, false, nil
	}
	var record edge.DeploymentRecord
	if err := json.Unmarshal(held.Bytes, &record); err != nil {
		return edge.DeploymentRecord{}, false, fmt.Errorf("decode the deployment record for %s/%s: %w", app, build, err)
	}
	return record, true, nil
}

func (l *Ledger) Promote(ctx context.Context, promotion edge.Promotion, pointer string, _ edge.Progress) error {
	name := pointerOr(pointer)
	claimed, err := l.claimTag(ctx, promotion)
	if err != nil {
		return err
	}
	seq, err := l.nextSequence(ctx)
	if err != nil {
		return err
	}
	at, err := records.ReadOrEmpty(ctx, l.records, l.pointerName(name))
	if err != nil {
		return fmt.Errorf("read what %s points at: %w", name, err)
	}
	holding, err := pointerOf(name, at)
	if err != nil {
		return err
	}
	held, err := records.ReadOrEmpty(ctx, l.records, l.promotionName(name, promotion.PromotionID))
	if err != nil {
		return fmt.Errorf("read promotion %s: %w", promotion.PromotionID, err)
	}
	row := promotionRecord{Promotion: promotion, Seq: seq, Claimed: claimed}
	if len(held.Bytes) > 0 {
		var prior promotionRecord
		if err := json.Unmarshal(held.Bytes, &prior); err != nil {
			return fmt.Errorf("decode promotion %s: %w", promotion.PromotionID, err)
		}
		row.Prior = &priorPosition{Seq: prior.Seq, Ts: prior.Ts}
	}
	encoded, err := json.Marshal(row)
	if err != nil {
		return fmt.Errorf("encode promotion %s: %w", promotion.PromotionID, err)
	}
	held.Bytes = encoded
	if _, err := l.records.Write(ctx, held); err != nil {
		return fmt.Errorf("record promotion %s: %w", promotion.PromotionID, err)
	}

	flipped, err := json.Marshal(holding.movedTo(promotion.PromotionID))
	if err != nil {
		return fmt.Errorf("encode the pointer %s: %w", name, err)
	}
	at.Bytes = flipped
	if _, err := l.records.Write(ctx, at); err != nil {
		if errors.Is(err, records.ErrStale) {
			holder, readErr := l.pointerAt(ctx, name)
			if readErr != nil || holder == "" {
				holder = "another promotion"
			}
			busy := refusal.Refuse(refusal.CodeBusy,
				"point %s at promotion %s: %s now holds %s, so another deploy moved it while this one was promoting, and this deploy stopped rather than overwrite it. The release it staged is still recorded: reach it with `ocel rollback --to %s`, or re-run this deploy once the other one has finished",
				name, promotion.PromotionID, holder, name, promotion.PromotionID)
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
		at, err := records.ReadOrEmpty(ctx, l.records, l.pointerName(name))
		if err != nil {
			return fmt.Errorf("read what %s points at: %w", name, err)
		}
		holding, err := pointerOf(name, at)
		if err != nil {
			return err
		}
		back, held := holding.without(promotionID)
		if !held {
			return l.retract(ctx, name, promotionID)
		}
		if back.PromotionID == "" {
			err = l.records.Remove(ctx, at.Name, at.Revision)
		} else {
			at.Bytes, err = json.Marshal(back)
			if err == nil {
				_, err = l.records.Write(ctx, at)
			}
		}
		if errors.Is(err, records.ErrStale) {
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
		held, err := records.ReadOrEmpty(ctx, l.records, l.promotionName(pointer, promotionID))
		if err != nil {
			return fmt.Errorf("read promotion %s: %w", promotionID, err)
		}
		if len(held.Bytes) == 0 {
			return fmt.Errorf("take back promotion %s: %s records no such promotion", promotionID, pointer)
		}
		var row promotionRecord
		if err := json.Unmarshal(held.Bytes, &row); err != nil {
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
		if held.Bytes, err = json.Marshal(row); err != nil {
			return fmt.Errorf("encode promotion %s: %w", promotionID, err)
		}
		if _, err := l.records.Write(ctx, held); err != nil {
			if errors.Is(err, records.ErrStale) {
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
		held, err := records.ReadOrEmpty(ctx, l.records, l.tagName(tag))
		if err != nil {
			return fmt.Errorf("read the tag %q: %w", tag, err)
		}
		if len(held.Bytes) == 0 {
			return nil
		}
		var claimed tagRecord
		if err := json.Unmarshal(held.Bytes, &claimed); err != nil {
			return fmt.Errorf("decode the tag %q: %w", tag, err)
		}
		if claimed.PromotionID != promotionID {
			return nil
		}
		err = l.records.Remove(ctx, held.Name, held.Revision)
		if errors.Is(err, records.ErrStale) {
			continue
		}
		if err != nil && !errors.Is(err, records.ErrNotFound) {
			return fmt.Errorf("free the tag %q: %w", tag, err)
		}
		return nil
	}
	return fmt.Errorf("free the tag %q: it moved under %d attempts", tag, casAttempts)
}

func (l *Ledger) claimTag(ctx context.Context, promotion edge.Promotion) (bool, error) {
	if promotion.Tag == "" {
		return false, nil
	}
	held, err := records.ReadOrEmpty(ctx, l.records, l.tagName(promotion.Tag))
	if err != nil {
		return false, fmt.Errorf("read the tag %q: %w", promotion.Tag, err)
	}
	if len(held.Bytes) > 0 {
		var claimed tagRecord
		if err := json.Unmarshal(held.Bytes, &claimed); err != nil {
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
	if _, err := l.records.Write(ctx, records.Record{Name: l.tagName(promotion.Tag), Bytes: encoded}); err != nil {
		if errors.Is(err, records.ErrStale) {
			return false, l.tagTaken(promotion, l.tagHolder(ctx, promotion.Tag))
		}
		return false, fmt.Errorf("claim the tag %q: %w", promotion.Tag, err)
	}
	return true, nil
}

func (l *Ledger) tagHolder(ctx context.Context, tag string) string {
	held, err := records.ReadOrEmpty(ctx, l.records, l.tagName(tag))
	if err != nil || len(held.Bytes) == 0 {
		return ""
	}
	var claimed tagRecord
	if err := json.Unmarshal(held.Bytes, &claimed); err != nil {
		return ""
	}
	return claimed.PromotionID
}

func (l *Ledger) tagTaken(promotion edge.Promotion, holder string) error {
	if holder == "" {
		holder = "another promotion"
	}
	return refusal.Refuse(refusal.CodeInvalid,
		"promote %s: the tag %q already names promotion %s in this project, and a tag names one release so that `ocel rollback --tag %s` is unambiguous. Pick another tag, or roll back to %s instead",
		promotion.PromotionID, promotion.Tag, holder, promotion.Tag, holder)
}

func (l *Ledger) nextSequence(ctx context.Context) (int64, error) {
	for range casAttempts {
		held, err := records.ReadOrEmpty(ctx, l.records, l.sequenceName())
		if err != nil {
			return 0, fmt.Errorf("read the promotion sequence for %s: %w", l.scope, err)
		}
		next := int64(1)
		if len(held.Bytes) > 0 {
			current, err := strconv.ParseInt(string(held.Bytes), 10, 64)
			if err != nil {
				return 0, fmt.Errorf("read the promotion sequence for %s: %w", l.scope, err)
			}
			next = current + 1
		}
		held.Bytes = []byte(strconv.FormatInt(next, 10))
		if _, err := l.records.Write(ctx, held); err != nil {
			if errors.Is(err, records.ErrStale) {
				continue
			}
			return 0, fmt.Errorf("advance the promotion sequence for %s: %w", l.scope, err)
		}
		return next, nil
	}
	return 0, fmt.Errorf("advance the promotion sequence for %s: it moved under %d attempts", l.scope, casAttempts)
}

func (l *Ledger) History(ctx context.Context, pointer string) ([]edge.HistoryEntry, error) {
	name := pointerOr(pointer)
	rows, err := l.promotions(ctx, name)
	if err != nil {
		return nil, err
	}
	active, err := l.pointerAt(ctx, name)
	if err != nil {
		return nil, err
	}
	entries := make([]edge.HistoryEntry, 0, len(rows))
	for _, row := range rows {
		entries = append(entries, edge.HistoryEntry{Promotion: row.Promotion, Active: row.PromotionID == active})
	}
	return entries, nil
}

func (l *Ledger) Prune(ctx context.Context, keepN int, pointer string) (edge.PruneResult, error) {
	name := pointerOr(pointer)
	rows, err := l.promotions(ctx, name)
	if err != nil {
		return edge.PruneResult{}, err
	}
	active, err := l.pointerAt(ctx, name)
	if err != nil {
		return edge.PruneResult{}, err
	}
	kept, removed := Retain(rows, keepN, active, func(row promotionRecord) string { return row.PromotionID })
	held := recordKeysOf(kept)
	if err := l.deletePromotions(ctx, name, removed, held); err != nil {
		return edge.PruneResult{}, err
	}
	surviving, err := l.recordKeys(ctx)
	if err != nil {
		return edge.PruneResult{}, err
	}
	return edge.PruneResult{
		KeptPromotionIDs:           promotionIDs(kept),
		RemovedPromotionIDs:        promotionIDs(removed),
		RemovedRecordKeys:          without(recordKeysOf(removed), held),
		SurvivingRecordKeys:        surviving,
		SurvivingPointerRecordKeys: held,
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

func (l *Ledger) RemovePointer(ctx context.Context, pointer string) (edge.PruneResult, error) {
	name := pointerOr(pointer)
	rows, err := l.promotions(ctx, name)
	if err != nil {
		return edge.PruneResult{}, err
	}
	if err := l.deletePromotions(ctx, name, rows, nil); err != nil {
		return edge.PruneResult{}, err
	}
	if err := records.Forget(ctx, l.records, l.pointerName(name)); err != nil {
		return edge.PruneResult{}, fmt.Errorf("forget pointer %s: %w", name, err)
	}
	surviving, err := l.recordKeys(ctx)
	if err != nil {
		return edge.PruneResult{}, err
	}
	return edge.PruneResult{
		RemovedPromotionIDs: promotionIDs(rows),
		RemovedRecordKeys:   recordKeysOf(rows),
		SurvivingRecordKeys: surviving,
	}, nil
}

func (l *Ledger) Pointers(ctx context.Context) ([]string, error) {
	held, err := l.records.List(ctx, l.pointersName())
	if err != nil {
		return nil, fmt.Errorf("read the pointers for %s: %w", l.scope, err)
	}
	names := make([]string, 0, len(held))
	for _, record := range held {
		rest, under := record.Name.Under(l.pointersName())
		if !under {
			continue
		}
		names = append(names, rest[0])
	}
	slices.Sort(names)
	return names, nil
}

func (l *Ledger) Destroy(ctx context.Context) error {
	held, err := l.records.List(ctx, l.name())
	if err != nil {
		return fmt.Errorf("read the deployments ledger for %s: %w", l.scope, err)
	}
	for _, record := range held {
		if err := records.Forget(ctx, l.records, record.Name); err != nil {
			return fmt.Errorf("erase the deployments ledger for %s: %w", l.scope, err)
		}
	}
	return nil
}

func (l *Ledger) NoteInvalidationTarget(ctx context.Context, distribution string) error {
	return l.setInvalidationTarget(ctx, distribution, true)
}

func (l *Ledger) ForgetInvalidationTarget(ctx context.Context, distribution string) error {
	return l.setInvalidationTarget(ctx, distribution, false)
}

func (l *Ledger) setInvalidationTarget(ctx context.Context, distribution string, note bool) error {
	if distribution == "" {
		return fmt.Errorf("note an invalidation target for %s: it names no front to invalidate", l.scope)
	}
	at := l.name("invalidation")
	for range casAttempts {
		held, err := records.ReadOrEmpty(ctx, l.records, at)
		if err != nil {
			return fmt.Errorf("read the invalidation targets for %s: %w", l.scope, err)
		}
		var targets []string
		if len(held.Bytes) > 0 {
			if err := json.Unmarshal(held.Bytes, &targets); err != nil {
				return fmt.Errorf("read the invalidation targets for %s: %w", l.scope, err)
			}
		}
		kept := slices.DeleteFunc(slices.Clone(targets), func(held string) bool { return held == distribution })
		if note {
			kept = append(kept, distribution)
		}
		slices.Sort(kept)
		if slices.Equal(kept, targets) {
			return nil
		}
		encoded, err := json.Marshal(kept)
		if err != nil {
			return fmt.Errorf("encode the invalidation targets for %s: %w", l.scope, err)
		}
		held.Bytes = encoded
		if _, err := l.records.Write(ctx, held); err != nil {
			if errors.Is(err, records.ErrStale) {
				continue
			}
			return fmt.Errorf("record the invalidation targets for %s: %w", l.scope, err)
		}
		return nil
	}
	return fmt.Errorf("record the invalidation targets for %s: they moved under %d attempts", l.scope, casAttempts)
}

func (l *Ledger) deletePromotions(ctx context.Context, pointer string, rows []promotionRecord, held []string) error {
	for _, row := range rows {
		names := []records.Name{l.promotionName(pointer, row.PromotionID)}
		for app, build := range row.Builds {
			if slices.Contains(held, RecordKey(app, build)) {
				continue
			}
			names = append(names, l.recordName(app, build))
		}
		if row.Tag != "" {
			names = append(names, l.tagName(row.Tag))
		}
		for _, name := range names {
			if err := records.Forget(ctx, l.records, name); err != nil {
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
	held, err := records.ReadOrEmpty(ctx, l.records, l.pointerName(pointer))
	if err != nil {
		return "", fmt.Errorf("read what %s points at: %w", pointer, err)
	}
	at, err := pointerOf(pointer, held)
	return at.PromotionID, err
}

func pointerOf(pointer string, held records.Record) (pointerRecord, error) {
	if len(held.Bytes) == 0 {
		return pointerRecord{}, nil
	}
	var at pointerRecord
	if err := json.Unmarshal(held.Bytes, &at); err != nil {
		return pointerRecord{}, fmt.Errorf("decode what %s points at: %w", pointer, err)
	}
	return at, nil
}

func (l *Ledger) promotions(ctx context.Context, pointer string) ([]promotionRecord, error) {
	held, err := l.records.List(ctx, l.promotionsName(pointer))
	if err != nil {
		return nil, fmt.Errorf("read the promotions for %s: %w", pointer, err)
	}
	rows := make([]promotionRecord, 0, len(held))
	for _, record := range held {
		var row promotionRecord
		if err := json.Unmarshal(record.Bytes, &row); err != nil {
			return nil, fmt.Errorf("decode promotion %s: %w", record.Name, err)
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
	held, err := l.records.List(ctx, l.recordsName())
	if err != nil {
		return nil, fmt.Errorf("read the deployment records for %s: %w", l.scope, err)
	}
	keys := make([]string, 0, len(held))
	for _, record := range held {
		rest, under := record.Name.Under(l.recordsName())
		if !under || len(rest) < 2 {
			continue
		}
		keys = append(keys, RecordKey(rest[0], strings.Join(rest[1:], "/")))
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

func without(keys, held []string) []string {
	return slices.DeleteFunc(slices.Clone(keys), func(key string) bool { return slices.Contains(held, key) })
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

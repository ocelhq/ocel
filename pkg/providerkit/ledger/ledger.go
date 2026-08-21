// Package ledger is an edge.Ledger over a providerkit.RecordStore, composed by
// an edge that hosts no ledger of its own — AWS's cloudfront and apigateway
// edges take it in place of the DynamoDB edgeledger they carry today, and
// Cloudflare keeps its Durable Object and composes nothing. Either way the kit
// reaches a ledger only through edge.EdgeStack and never through records
// directly, so [#391]'s "ownership is composition" holds with no second path
// into the same memory.
//
// The split of duties is the contract's: keep-N selection is this package's
// (and the DO's), while N itself and the reclamation sweep that follows a prune
// are the kit's.
//
// [#391]: https://github.com/ocelhq/ocel/issues/391
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

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const casAttempts = 8

type Ledger struct {
	records providerkit.RecordStore
	scope   string
}

var _ edge.Ledger = (*Ledger)(nil)

func New(records providerkit.RecordStore, class providerkit.Class, slug string) *Ledger {
	return &Ledger{records: records, scope: Scope(class, slug)}
}

// Scope partitions one ledger from every other in the account. It is the edge
// contract's class, narrowed by project where there is one, and it matches what
// the DynamoDB ledger already spells so a migration is a copy and not a rename.
func Scope(class providerkit.Class, slug string) string {
	if slug == "" {
		return string(class)
	}
	return string(class) + naming.PathSeparator + naming.Sanitize(slug)
}

func RecordKey(app, identity string) string { return "record:" + app + "/" + identity }

func (l *Ledger) name(rest ...string) providerkit.RecordName {
	return providerkit.LedgerRecord(l.scope, rest...)
}

func (l *Ledger) schemaName() providerkit.RecordName   { return l.name("schema") }
func (l *Ledger) sequenceName() providerkit.RecordName { return l.name("seq") }
func (l *Ledger) pointerName(n string) providerkit.RecordName {
	return l.name("pointers", n)
}
func (l *Ledger) promotionsName(pointer string) providerkit.RecordName {
	return l.name("promotions", pointer)
}
func (l *Ledger) promotionName(pointer, id string) providerkit.RecordName {
	return l.name("promotions", pointer, id)
}
func (l *Ledger) recordName(app, identity string) providerkit.RecordName {
	return l.name("records", app, identity)
}
func (l *Ledger) tagName(tag string) providerkit.RecordName { return l.name("tags", tag) }

func pointerOr(pointer string) string {
	if pointer == "" {
		return edge.DefaultPointer
	}
	return pointer
}

type promotionRecord struct {
	edge.Promotion
	Seq int64 `json:"seq"`
}

type pointerRecord struct {
	PromotionID string `json:"promotionId"`
}

type tagRecord struct {
	PromotionID string `json:"promotionId"`
}

func (l *Ledger) EnsureSchema(ctx context.Context) error {
	held, err := l.records.Read(ctx, l.schemaName())
	if err != nil {
		return fmt.Errorf("read the deployments ledger schema for %s: %w", l.scope, err)
	}
	held.Name, held.Bytes = l.schemaName(), []byte(strconv.Itoa(edge.StoreSchemaVersion))
	if _, err := l.records.Write(ctx, held); err != nil {
		return fmt.Errorf("record the deployments ledger schema for %s: %w", l.scope, err)
	}
	return nil
}

func (l *Ledger) SchemaVersion(ctx context.Context) (int, error) {
	held, err := l.records.Read(ctx, l.schemaName())
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
	if record.App == "" || record.Identity == "" {
		return fmt.Errorf("stage a deployment record: it names app %q and identity %q, and the ledger keys records by both", record.App, record.Identity)
	}
	name := l.recordName(record.App, record.Identity)
	held, err := l.records.Read(ctx, name)
	if err != nil {
		return fmt.Errorf("read the deployment record for %s: %w", record.App, err)
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode the deployment record for %s: %w", record.App, err)
	}
	held.Name, held.Bytes = name, encoded
	if _, err := l.records.Write(ctx, held); err != nil {
		return fmt.Errorf("stage the deployment record for %s: %w", record.App, err)
	}
	return nil
}

func (l *Ledger) Record(ctx context.Context, app, identity string) (edge.DeploymentRecord, bool, error) {
	held, err := l.records.Read(ctx, l.recordName(app, identity))
	if err != nil {
		return edge.DeploymentRecord{}, false, fmt.Errorf("read the deployment record for %s/%s: %w", app, identity, err)
	}
	if len(held.Bytes) == 0 {
		return edge.DeploymentRecord{}, false, nil
	}
	var record edge.DeploymentRecord
	if err := json.Unmarshal(held.Bytes, &record); err != nil {
		return edge.DeploymentRecord{}, false, fmt.Errorf("decode the deployment record for %s/%s: %w", app, identity, err)
	}
	return record, true, nil
}

// Promote claims the tag, records the promotion, then flips the pointer with the
// revision it was read at. The flip is the compare-and-set the whole store
// exists for: losing it means another deploy moved the pointer, and this one
// stops rather than overwrite it.
func (l *Ledger) Promote(ctx context.Context, promotion edge.Promotion, pointer string) error {
	name := pointerOr(pointer)
	if err := l.claimTag(ctx, promotion); err != nil {
		return err
	}
	seq, err := l.nextSequence(ctx)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(promotionRecord{Promotion: promotion, Seq: seq})
	if err != nil {
		return fmt.Errorf("encode promotion %s: %w", promotion.PromotionID, err)
	}
	if _, err := l.records.Write(ctx, providerkit.Record{
		Name:  l.promotionName(name, promotion.PromotionID),
		Bytes: encoded,
	}); err != nil {
		return fmt.Errorf("record promotion %s: %w", promotion.PromotionID, err)
	}

	held, err := l.records.Read(ctx, l.pointerName(name))
	if err != nil {
		return fmt.Errorf("read what %s points at: %w", name, err)
	}
	flipped, err := json.Marshal(pointerRecord{PromotionID: promotion.PromotionID})
	if err != nil {
		return fmt.Errorf("encode the pointer %s: %w", name, err)
	}
	held.Name, held.Bytes = l.pointerName(name), flipped
	if _, err := l.records.Write(ctx, held); err != nil {
		if errors.Is(err, providerkit.ErrStale) {
			holder, readErr := l.pointerAt(ctx, name)
			if readErr != nil || holder == "" {
				holder = "another promotion"
			}
			return providerkit.Refuse(providerkit.CodeBusy,
				"point %s at promotion %s: %s now holds %s, so another deploy moved it while this one was promoting, and this deploy stopped rather than overwrite it. The release it staged is still recorded: reach it with `ocel rollback --to %s`, or re-run this deploy once the other one has finished",
				name, promotion.PromotionID, holder, name, promotion.PromotionID)
		}
		return fmt.Errorf("point %s at promotion %s: %w", name, promotion.PromotionID, err)
	}
	return nil
}

// claimTag is a put-if-absent: a zero Revision means "must not exist yet", so
// ErrStale here is a second claimant and not a race to retry.
func (l *Ledger) claimTag(ctx context.Context, promotion edge.Promotion) error {
	if promotion.Tag == "" {
		return nil
	}
	held, err := l.records.Read(ctx, l.tagName(promotion.Tag))
	if err != nil {
		return fmt.Errorf("read the tag %q: %w", promotion.Tag, err)
	}
	if len(held.Bytes) > 0 {
		var claimed tagRecord
		if err := json.Unmarshal(held.Bytes, &claimed); err != nil {
			return fmt.Errorf("decode the tag %q: %w", promotion.Tag, err)
		}
		if claimed.PromotionID == promotion.PromotionID {
			return nil
		}
		return l.tagTaken(promotion, claimed.PromotionID)
	}
	encoded, err := json.Marshal(tagRecord{PromotionID: promotion.PromotionID})
	if err != nil {
		return fmt.Errorf("encode the tag %q: %w", promotion.Tag, err)
	}
	if _, err := l.records.Write(ctx, providerkit.Record{Name: l.tagName(promotion.Tag), Bytes: encoded}); err != nil {
		if errors.Is(err, providerkit.ErrStale) {
			return l.tagTaken(promotion, l.tagHolder(ctx, promotion.Tag))
		}
		return fmt.Errorf("claim the tag %q: %w", promotion.Tag, err)
	}
	return nil
}

func (l *Ledger) tagHolder(ctx context.Context, tag string) string {
	held, err := l.records.Read(ctx, l.tagName(tag))
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
	return providerkit.Refuse(providerkit.CodeOccupied,
		"promote %s: the tag %q already names promotion %s in this project, and a tag names one release so that `ocel rollback --tag %s` is unambiguous. Pick another tag, or roll back to %s instead",
		promotion.PromotionID, promotion.Tag, holder, promotion.Tag, holder)
}

// nextSequence is the CAS loop the store's four verbs make possible without an
// atomic counter: read, add one, write at the revision read. ErrStale means a
// concurrent claimer won, and the retry re-reads its value.
func (l *Ledger) nextSequence(ctx context.Context) (int64, error) {
	for attempt := 0; attempt < casAttempts; attempt++ {
		held, err := l.records.Read(ctx, l.sequenceName())
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
		held.Name, held.Bytes = l.sequenceName(), []byte(strconv.FormatInt(next, 10))
		if _, err := l.records.Write(ctx, held); err != nil {
			if errors.Is(err, providerkit.ErrStale) {
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

// Prune keeps the newest N and whatever the pointer is on, which is the
// contract's selection rule: the active release is never a candidate however far
// down the history it has fallen.
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
	var kept, removed []promotionRecord
	for i, row := range rows {
		if i < keepN || row.PromotionID == active {
			kept = append(kept, row)
			continue
		}
		removed = append(removed, row)
	}
	if err := l.drop(ctx, name, removed); err != nil {
		return edge.PruneResult{}, err
	}
	surviving, err := l.recordKeys(ctx)
	if err != nil {
		return edge.PruneResult{}, err
	}
	return edge.PruneResult{
		KeptPromotionIDs:           promotionIDs(kept),
		RemovedPromotionIDs:        promotionIDs(removed),
		RemovedRecordKeys:          recordKeysOf(removed),
		SurvivingRecordKeys:        surviving,
		SurvivingPointerRecordKeys: recordKeysOf(kept),
	}, nil
}

func (l *Ledger) RemovePointer(ctx context.Context, pointer string) (edge.PruneResult, error) {
	name := pointerOr(pointer)
	rows, err := l.promotions(ctx, name)
	if err != nil {
		return edge.PruneResult{}, err
	}
	if err := l.drop(ctx, name, rows); err != nil {
		return edge.PruneResult{}, err
	}
	if err := l.forget(ctx, l.pointerName(name)); err != nil {
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
	held, err := l.records.List(ctx, l.name("pointers"))
	if err != nil {
		return nil, fmt.Errorf("read the pointers for %s: %w", l.scope, err)
	}
	names := make([]string, 0, len(held))
	for _, record := range held {
		names = append(names, record.Name[len(record.Name)-1])
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
		if err := l.records.Remove(ctx, record.Name, record.Revision); err != nil && !errors.Is(err, providerkit.ErrStale) {
			return fmt.Errorf("erase the deployments ledger for %s: %w", l.scope, err)
		}
	}
	return nil
}

func (l *Ledger) drop(ctx context.Context, pointer string, rows []promotionRecord) error {
	for _, row := range rows {
		if err := l.forget(ctx, l.promotionName(pointer, row.PromotionID)); err != nil {
			return fmt.Errorf("drop the promotions %s no longer keeps: %w", pointer, err)
		}
		for app, identity := range row.Builds {
			if err := l.forget(ctx, l.recordName(app, identity)); err != nil {
				return fmt.Errorf("drop the promotions %s no longer keeps: %w", pointer, err)
			}
		}
		if row.Tag != "" {
			if err := l.forget(ctx, l.tagName(row.Tag)); err != nil {
				return fmt.Errorf("drop the promotions %s no longer keeps: %w", pointer, err)
			}
		}
	}
	return nil
}

func (l *Ledger) forget(ctx context.Context, name providerkit.RecordName) error {
	held, err := l.records.Read(ctx, name)
	if err != nil {
		return err
	}
	if err := l.records.Remove(ctx, name, held.Revision); err != nil && !errors.Is(err, providerkit.ErrStale) {
		return err
	}
	return nil
}

func (l *Ledger) pointerAt(ctx context.Context, pointer string) (string, error) {
	held, err := l.records.Read(ctx, l.pointerName(pointer))
	if err != nil {
		return "", fmt.Errorf("read what %s points at: %w", pointer, err)
	}
	if len(held.Bytes) == 0 {
		return "", nil
	}
	var at pointerRecord
	if err := json.Unmarshal(held.Bytes, &at); err != nil {
		return "", fmt.Errorf("decode what %s points at: %w", pointer, err)
	}
	return at.PromotionID, nil
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
	held, err := l.records.List(ctx, l.name("records"))
	if err != nil {
		return nil, fmt.Errorf("read the deployment records for %s: %w", l.scope, err)
	}
	keys := make([]string, 0, len(held))
	for _, record := range held {
		rest := record.Name[len(l.name("records")):]
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

func recordKeysOf(rows []promotionRecord) []string {
	seen := map[string]bool{}
	var keys []string
	for _, row := range rows {
		for app, identity := range row.Builds {
			k := RecordKey(app, identity)
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

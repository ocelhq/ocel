package edgeledger

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	partition = "EDGELEDGER"

	DefaultPointer = "@production"

	schemaKey     = "META#schema"
	sequenceKey   = "META#seq"
	pointerStem   = "POINTER#"
	promotionStem = "PROMOTION#"
	recordStem    = "RECORD#"

	recordKeyPrefix = "record:"

	batchWriteLimit = 25

	unprocessedAttempts = 8
)

type DynamoAPI interface {
	GetItem(context.Context, *dynamodb.GetItemInput, ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error)
	PutItem(context.Context, *dynamodb.PutItemInput, ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error)
	DeleteItem(context.Context, *dynamodb.DeleteItemInput, ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error)
	UpdateItem(context.Context, *dynamodb.UpdateItemInput, ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error)
	Query(context.Context, *dynamodb.QueryInput, ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error)
	BatchWriteItem(context.Context, *dynamodb.BatchWriteItemInput, ...func(*dynamodb.Options)) (*dynamodb.BatchWriteItemOutput, error)
}

type Ledger struct {
	Dynamo DynamoAPI
	Table  string
	Scope  string
}

var _ edge.Ledger = (*Ledger)(nil)

func RecordKey(app, identity string) string {
	return recordKeyPrefix + app + "/" + identity
}

func (l *Ledger) partition() string { return partition + "#" + l.Scope }

func (l *Ledger) ready() error {
	if l.Dynamo == nil {
		return fmt.Errorf("the deployments ledger has no DynamoDB client; bootstrap the account first")
	}
	if l.Table == "" {
		return fmt.Errorf("the deployments ledger names no state table; bootstrap the account first")
	}
	if l.Scope == "" {
		return fmt.Errorf("the deployments ledger names no project scope")
	}
	return nil
}

func pointerOr(pointer string) string {
	if pointer == "" {
		return DefaultPointer
	}
	return pointer
}

func (l *Ledger) EnsureSchema(ctx context.Context) error {
	if err := l.ready(); err != nil {
		return err
	}
	_, err := l.Dynamo.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(l.Table),
		Item: l.item(schemaKey, map[string]ddbtypes.AttributeValue{
			"schema": number(int64(edge.StoreSchemaVersion)),
		}),
	})
	if err != nil {
		return fmt.Errorf("record the deployments ledger schema for %s: %w", l.Scope, err)
	}
	return nil
}

func (l *Ledger) SchemaVersion(ctx context.Context) (int, error) {
	if err := l.ready(); err != nil {
		return 0, err
	}
	out, err := l.Dynamo.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:      aws.String(l.Table),
		ConsistentRead: aws.Bool(true),
		Key:            key(l.partition(), schemaKey),
	})
	if err != nil {
		return 0, fmt.Errorf("read the deployments ledger schema for %s: %w", l.Scope, err)
	}
	recorded, ok := out.Item["schema"].(*ddbtypes.AttributeValueMemberN)
	if !ok {
		return 0, edge.ErrStoreSchemaUnreadable
	}
	version, err := strconv.Atoi(recorded.Value)
	if err != nil {
		return 0, fmt.Errorf("read the deployments ledger schema for %s: %w", l.Scope, err)
	}
	return version, nil
}

func (l *Ledger) PutStaged(ctx context.Context, record edge.DeploymentRecord) error {
	if err := l.ready(); err != nil {
		return err
	}
	if record.App == "" || record.Identity == "" {
		return fmt.Errorf("stage a deployment record: it names app %q and identity %q, and the ledger keys records by both", record.App, record.Identity)
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode the deployment record for %s: %w", record.App, err)
	}
	_, err = l.Dynamo.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(l.Table),
		Item: l.item(recordStem+record.App+"#"+record.Identity, map[string]ddbtypes.AttributeValue{
			"app":      text(record.App),
			"identity": text(record.Identity),
			"data":     text(string(encoded)),
		}),
	})
	if err != nil {
		return fmt.Errorf("stage the deployment record for %s: %w", record.App, err)
	}
	return nil
}

func (l *Ledger) Record(ctx context.Context, app, identity string) (edge.DeploymentRecord, bool, error) {
	if err := l.ready(); err != nil {
		return edge.DeploymentRecord{}, false, err
	}
	out, err := l.Dynamo.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:      aws.String(l.Table),
		ConsistentRead: aws.Bool(true),
		Key:            key(l.partition(), recordStem+app+"#"+identity),
	})
	if err != nil {
		return edge.DeploymentRecord{}, false, fmt.Errorf("read the deployment record for %s/%s: %w", app, identity, err)
	}
	data, ok := out.Item["data"].(*ddbtypes.AttributeValueMemberS)
	if !ok {
		return edge.DeploymentRecord{}, false, nil
	}
	var record edge.DeploymentRecord
	if err := json.Unmarshal([]byte(data.Value), &record); err != nil {
		return edge.DeploymentRecord{}, false, fmt.Errorf("decode the deployment record for %s/%s: %w", app, identity, err)
	}
	return record, true, nil
}

func (l *Ledger) Promote(ctx context.Context, promotion edge.Promotion, pointer string) error {
	if err := l.ready(); err != nil {
		return err
	}
	name := pointerOr(pointer)
	if err := l.checkTag(ctx, promotion); err != nil {
		return err
	}
	held, err := l.pointerAt(ctx, name)
	if err != nil {
		return err
	}
	seq, err := l.nextSequence(ctx)
	if err != nil {
		return err
	}
	builds, err := json.Marshal(promotion.Builds)
	if err != nil {
		return fmt.Errorf("encode the builds promotion %s carries: %w", promotion.PromotionID, err)
	}
	attrs := map[string]ddbtypes.AttributeValue{
		"pointer":     text(name),
		"promotionId": text(promotion.PromotionID),
		"ts":          number(promotion.Ts),
		"builds":      text(string(builds)),
		"seq":         number(seq),
	}
	if promotion.Tag != "" {
		attrs["tag"] = text(promotion.Tag)
	}
	if _, err := l.Dynamo.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(l.Table),
		Item:      l.item(promotionStem+name+"#"+promotion.PromotionID, attrs),
	}); err != nil {
		return fmt.Errorf("record promotion %s: %w", promotion.PromotionID, err)
	}

	move := &dynamodb.PutItemInput{
		TableName: aws.String(l.Table),
		Item: l.item(pointerStem+name, map[string]ddbtypes.AttributeValue{
			"promotionId": text(promotion.PromotionID),
		}),
		ExpressionAttributeNames: map[string]string{"#promotionId": "promotionId"},
	}
	if held == "" {
		move.ConditionExpression = aws.String("attribute_not_exists(#promotionId)")
	} else {
		move.ConditionExpression = aws.String("#promotionId = :held")
		move.ExpressionAttributeValues = map[string]ddbtypes.AttributeValue{":held": text(held)}
	}
	if _, err := l.Dynamo.PutItem(ctx, move); err != nil {
		var raced *ddbtypes.ConditionalCheckFailedException
		if errors.As(err, &raced) {
			holder, readErr := l.pointerAt(ctx, name)
			if readErr != nil || holder == "" {
				holder = "another promotion"
			}
			return fmt.Errorf("point %s at promotion %s: %s now holds %s, so another deploy moved it while this one was promoting, and this deploy stopped rather than overwrite it. The release it staged is still recorded: reach it with `ocel rollback --to %s`, or re-run this deploy once the other one has finished", name, promotion.PromotionID, holder, name, promotion.PromotionID)
		}
		return fmt.Errorf("point %s at promotion %s: %w", name, promotion.PromotionID, err)
	}
	return nil
}

func (l *Ledger) checkTag(ctx context.Context, promotion edge.Promotion) error {
	if promotion.Tag == "" {
		return nil
	}
	items, err := l.query(ctx, promotionStem)
	if err != nil {
		return err
	}
	for _, entry := range items {
		if str(entry, "tag") != promotion.Tag {
			continue
		}
		if id := promotionIDOf(entry); id != "" && id != promotion.PromotionID {
			return fmt.Errorf("promote %s: the tag %q already names promotion %s in this project, and a tag names one release so that `ocel rollback --tag %s` is unambiguous. Pick another tag, or roll back to %s instead", promotion.PromotionID, promotion.Tag, id, promotion.Tag, id)
		}
	}
	return nil
}

func (l *Ledger) History(ctx context.Context, pointer string) ([]edge.HistoryEntry, error) {
	if err := l.ready(); err != nil {
		return nil, err
	}
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
		entries = append(entries, edge.HistoryEntry{Promotion: row.promotion(), Active: row.id == active})
	}
	return entries, nil
}

func (l *Ledger) Prune(ctx context.Context, keepN int, pointer string) (edge.PruneResult, error) {
	if err := l.ready(); err != nil {
		return edge.PruneResult{}, err
	}
	name := pointerOr(pointer)
	rows, err := l.promotions(ctx, name)
	if err != nil {
		return edge.PruneResult{}, err
	}
	active, err := l.pointerAt(ctx, name)
	if err != nil {
		return edge.PruneResult{}, err
	}

	var kept, removed []promotionRow
	for i, row := range rows {
		if i < keepN || row.id == active {
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
	if err := l.ready(); err != nil {
		return edge.PruneResult{}, err
	}
	name := pointerOr(pointer)
	rows, err := l.promotions(ctx, name)
	if err != nil {
		return edge.PruneResult{}, err
	}
	if err := l.drop(ctx, name, rows); err != nil {
		return edge.PruneResult{}, err
	}
	if _, err := l.Dynamo.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(l.Table),
		Key:       key(l.partition(), pointerStem+name),
	}); err != nil {
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

func (l *Ledger) Destroy(ctx context.Context) error {
	if err := l.ready(); err != nil {
		return err
	}
	items, err := l.query(ctx, "")
	if err != nil {
		return err
	}
	sortKeys := make([]string, 0, len(items))
	for _, entry := range items {
		if sk := str(entry, "sk"); sk != "" {
			sortKeys = append(sortKeys, sk)
		}
	}
	if err := l.erase(ctx, sortKeys); err != nil {
		return fmt.Errorf("erase the deployments ledger for %s: %w", l.Scope, err)
	}
	return nil
}

func (l *Ledger) drop(ctx context.Context, pointer string, rows []promotionRow) error {
	var sortKeys []string
	for _, row := range rows {
		sortKeys = append(sortKeys, promotionStem+pointer+"#"+row.id)
		for app, identity := range row.builds {
			sortKeys = append(sortKeys, recordStem+app+"#"+identity)
		}
	}
	if err := l.erase(ctx, sortKeys); err != nil {
		return fmt.Errorf("drop the promotions %s no longer keeps: %w", pointer, err)
	}
	return nil
}

func (l *Ledger) erase(ctx context.Context, sortKeys []string) error {
	slices.Sort(sortKeys)
	sortKeys = slices.Compact(sortKeys)
	for batch := range slices.Chunk(sortKeys, batchWriteLimit) {
		requests := make([]ddbtypes.WriteRequest, 0, len(batch))
		for _, sk := range batch {
			requests = append(requests, ddbtypes.WriteRequest{
				DeleteRequest: &ddbtypes.DeleteRequest{Key: key(l.partition(), sk)},
			})
		}
		if err := l.write(ctx, requests); err != nil {
			return err
		}
	}
	return nil
}

func (l *Ledger) write(ctx context.Context, requests []ddbtypes.WriteRequest) error {
	pending := map[string][]ddbtypes.WriteRequest{l.Table: requests}
	for attempt := 0; attempt < unprocessedAttempts; attempt++ {
		out, err := l.Dynamo.BatchWriteItem(ctx, &dynamodb.BatchWriteItemInput{RequestItems: pending})
		if err != nil {
			return err
		}
		left := out.UnprocessedItems[l.Table]
		if len(left) == 0 {
			return nil
		}
		pending = map[string][]ddbtypes.WriteRequest{l.Table: left}
	}
	return fmt.Errorf("%d writes were still unprocessed after %d attempts", len(pending[l.Table]), unprocessedAttempts)
}

func (l *Ledger) nextSequence(ctx context.Context) (int64, error) {
	out, err := l.Dynamo.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:                 aws.String(l.Table),
		Key:                       key(l.partition(), sequenceKey),
		UpdateExpression:          aws.String("ADD #seq :one"),
		ExpressionAttributeNames:  map[string]string{"#seq": "seq"},
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{":one": number(1)},
		ReturnValues:              ddbtypes.ReturnValueUpdatedNew,
	})
	if err != nil {
		return 0, fmt.Errorf("advance the promotion sequence for %s: %w", l.Scope, err)
	}
	seq, ok := out.Attributes["seq"].(*ddbtypes.AttributeValueMemberN)
	if !ok {
		return 0, fmt.Errorf("advance the promotion sequence for %s: the counter reported no value", l.Scope)
	}
	return strconv.ParseInt(seq.Value, 10, 64)
}

type promotionRow struct {
	id     string
	ts     int64
	tag    string
	seq    int64
	builds map[string]string
}

func (r promotionRow) promotion() edge.Promotion {
	return edge.Promotion{PromotionID: r.id, Ts: r.ts, Builds: r.builds, Tag: r.tag}
}

func promotionIDs(rows []promotionRow) []string {
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.id)
	}
	return ids
}

func recordKeysOf(rows []promotionRow) []string {
	seen := map[string]bool{}
	var keys []string
	for _, row := range rows {
		for app, identity := range row.builds {
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

func promotionIDOf(entry map[string]ddbtypes.AttributeValue) string {
	sk := str(entry, "sk")
	if !strings.HasPrefix(sk, promotionStem) {
		return ""
	}
	_, id, found := strings.Cut(strings.TrimPrefix(sk, promotionStem), "#")
	if !found {
		return ""
	}
	return id
}

func (l *Ledger) promotions(ctx context.Context, pointer string) ([]promotionRow, error) {
	items, err := l.query(ctx, promotionStem+pointer+"#")
	if err != nil {
		return nil, err
	}
	rows := make([]promotionRow, 0, len(items))
	for _, entry := range items {
		row := promotionRow{
			id:  promotionIDOf(entry),
			ts:  num(entry, "ts"),
			tag: str(entry, "tag"),
			seq: num(entry, "seq"),
		}
		if raw := str(entry, "builds"); raw != "" {
			if err := json.Unmarshal([]byte(raw), &row.builds); err != nil {
				return nil, fmt.Errorf("decode the builds promotion %s carries: %w", row.id, err)
			}
		}
		rows = append(rows, row)
	}
	slices.SortFunc(rows, func(a, b promotionRow) int {
		if n := cmp.Compare(b.seq, a.seq); n != 0 {
			return n
		}
		return cmp.Compare(b.id, a.id)
	})
	return rows, nil
}

func (l *Ledger) pointerAt(ctx context.Context, pointer string) (string, error) {
	out, err := l.Dynamo.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:      aws.String(l.Table),
		ConsistentRead: aws.Bool(true),
		Key:            key(l.partition(), pointerStem+pointer),
	})
	if err != nil {
		return "", fmt.Errorf("read what %s points at: %w", pointer, err)
	}
	return str(out.Item, "promotionId"), nil
}

func (l *Ledger) Pointers(ctx context.Context) ([]string, error) {
	if err := l.ready(); err != nil {
		return nil, err
	}
	items, err := l.query(ctx, pointerStem)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(items))
	for _, entry := range items {
		names = append(names, strings.TrimPrefix(str(entry, "sk"), pointerStem))
	}
	slices.Sort(names)
	return names, nil
}

func (l *Ledger) recordKeys(ctx context.Context) ([]string, error) {
	items, err := l.query(ctx, recordStem)
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(items))
	for _, entry := range items {
		keys = append(keys, RecordKey(str(entry, "app"), str(entry, "identity")))
	}
	slices.Sort(keys)
	return keys, nil
}

func (l *Ledger) query(ctx context.Context, prefix string) ([]map[string]ddbtypes.AttributeValue, error) {
	condition := "#pk = :pk"
	values := map[string]ddbtypes.AttributeValue{":pk": text(l.partition())}
	names := map[string]string{"#pk": "pk"}
	if prefix != "" {
		condition += " AND begins_with(#sk, :prefix)"
		names["#sk"] = "sk"
		values[":prefix"] = text(prefix)
	}

	var (
		out   []map[string]ddbtypes.AttributeValue
		start map[string]ddbtypes.AttributeValue
	)
	for {
		page, err := l.Dynamo.Query(ctx, &dynamodb.QueryInput{
			TableName:                 aws.String(l.Table),
			ConsistentRead:            aws.Bool(true),
			KeyConditionExpression:    aws.String(condition),
			ExpressionAttributeNames:  names,
			ExpressionAttributeValues: values,
			ExclusiveStartKey:         start,
		})
		if err != nil {
			return nil, fmt.Errorf("read the deployments ledger for %s: %w", l.Scope, err)
		}
		out = append(out, page.Items...)
		if len(page.LastEvaluatedKey) == 0 {
			return out, nil
		}
		start = page.LastEvaluatedKey
	}
}

func (l *Ledger) item(sk string, attrs map[string]ddbtypes.AttributeValue) map[string]ddbtypes.AttributeValue {
	entry := key(l.partition(), sk)
	for name, value := range attrs {
		entry[name] = value
	}
	return entry
}

func key(pk, sk string) map[string]ddbtypes.AttributeValue {
	return map[string]ddbtypes.AttributeValue{"pk": text(pk), "sk": text(sk)}
}

func text(value string) ddbtypes.AttributeValue {
	return &ddbtypes.AttributeValueMemberS{Value: value}
}

func number(value int64) ddbtypes.AttributeValue {
	return &ddbtypes.AttributeValueMemberN{Value: strconv.FormatInt(value, 10)}
}

func str(entry map[string]ddbtypes.AttributeValue, name string) string {
	value, ok := entry[name].(*ddbtypes.AttributeValueMemberS)
	if !ok {
		return ""
	}
	return value.Value
}

func num(entry map[string]ddbtypes.AttributeValue, name string) int64 {
	value, ok := entry[name].(*ddbtypes.AttributeValueMemberN)
	if !ok {
		return 0
	}
	parsed, err := strconv.ParseInt(value.Value, 10, 64)
	if err != nil {
		return 0
	}
	return parsed
}

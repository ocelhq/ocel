package edgeledger

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	table = "ocel-state"
	scope = "production/shop"
)

type fakeDynamo struct {
	mu    sync.Mutex
	items map[string]map[string]ddbtypes.AttributeValue
	calls []string

	pageSize  int
	defer1st  bool
	beforePut func(key string)
}

func newFakeDynamo() *fakeDynamo {
	return &fakeDynamo{items: map[string]map[string]ddbtypes.AttributeValue{}, pageSize: 1}
}

func ledgerOn(dynamo DynamoAPI) *Ledger {
	return &Ledger{Dynamo: dynamo, Table: table, Scope: scope}
}

func itemKey(item map[string]ddbtypes.AttributeValue) string {
	pk, _ := item["pk"].(*ddbtypes.AttributeValueMemberS)
	sk, _ := item["sk"].(*ddbtypes.AttributeValueMemberS)
	if pk == nil || sk == nil {
		return ""
	}
	return pk.Value + "\x00" + sk.Value
}

func (f *fakeDynamo) count(verb string) int {
	n := 0
	for _, call := range f.calls {
		if call == verb || strings.HasPrefix(call, verb+" ") {
			n++
		}
	}
	return n
}

func (f *fakeDynamo) sortKeys() []string {
	var keys []string
	for _, key := range slices.Sorted(maps.Keys(f.items)) {
		_, sk, _ := strings.Cut(key, "\x00")
		keys = append(keys, sk)
	}
	return keys
}

func (f *fakeDynamo) GetItem(_ context.Context, in *dynamodb.GetItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if aws.ToString(in.TableName) != table {
		return nil, fmt.Errorf("fake dynamo holds %s, got a read of %q", table, aws.ToString(in.TableName))
	}
	f.calls = append(f.calls, "GetItem")
	return &dynamodb.GetItemOutput{Item: f.items[itemKey(in.Key)]}, nil
}

func (f *fakeDynamo) PutItem(_ context.Context, in *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := itemKey(in.Item)
	if f.beforePut != nil {
		f.beforePut(key)
	}
	held, err := conditionHolds(in, f.items[key])
	if err != nil {
		return nil, err
	}
	if !held {
		return nil, &ddbtypes.ConditionalCheckFailedException{Message: aws.String("condition on " + key)}
	}
	f.calls = append(f.calls, "PutItem")
	f.items[key] = in.Item
	return &dynamodb.PutItemOutput{}, nil
}

func conditionHolds(in *dynamodb.PutItemInput, present map[string]ddbtypes.AttributeValue) (bool, error) {
	switch condition := aws.ToString(in.ConditionExpression); condition {
	case "":
		return true, nil
	case "attribute_not_exists(#promotionId)":
		_, taken := present["promotionId"]
		return !taken, nil
	case "#promotionId = :held":
		want, ok := in.ExpressionAttributeValues[":held"].(*ddbtypes.AttributeValueMemberS)
		if !ok {
			return false, fmt.Errorf("fake dynamo needs the value the condition %q compares against", condition)
		}
		got, ok := present["promotionId"].(*ddbtypes.AttributeValueMemberS)
		return ok && got.Value == want.Value, nil
	default:
		return false, fmt.Errorf("fake dynamo does not speak the condition %q", condition)
	}
}

func (f *fakeDynamo) DeleteItem(_ context.Context, in *dynamodb.DeleteItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "DeleteItem")
	delete(f.items, itemKey(in.Key))
	return &dynamodb.DeleteItemOutput{}, nil
}

func (f *fakeDynamo) BatchWriteItem(_ context.Context, in *dynamodb.BatchWriteItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.BatchWriteItemOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "BatchWriteItem")
	unprocessed := map[string][]ddbtypes.WriteRequest{}
	for name, requests := range in.RequestItems {
		if name != table {
			return nil, fmt.Errorf("fake dynamo holds %s, got a batch for %q", table, name)
		}
		if len(requests) > batchWriteLimit {
			return nil, fmt.Errorf("fake dynamo got %d writes, more than the %d BatchWriteItem takes", len(requests), batchWriteLimit)
		}
		if f.defer1st {
			f.defer1st = false
			unprocessed[name] = requests[:1]
			requests = requests[1:]
		}
		for _, request := range requests {
			if request.DeleteRequest == nil {
				return nil, fmt.Errorf("fake dynamo batches only deletes, got %+v", request)
			}
			delete(f.items, itemKey(request.DeleteRequest.Key))
		}
	}
	return &dynamodb.BatchWriteItemOutput{UnprocessedItems: unprocessed}, nil
}

func (f *fakeDynamo) UpdateItem(_ context.Context, in *dynamodb.UpdateItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	expression := aws.ToString(in.UpdateExpression)
	if expression != "ADD #seq :one" && expression != "ADD #targets :target" && expression != "DELETE #targets :target" {
		return nil, fmt.Errorf("fake dynamo understands only the sequence counter and the invalidation targets, got %q", expression)
	}
	f.calls = append(f.calls, "UpdateItem")
	key := itemKey(in.Key)
	item, ok := f.items[key]
	if !ok {
		item = maps.Clone(in.Key)
	}
	if strings.HasSuffix(expression, "#targets :target") {
		changed, ok := in.ExpressionAttributeValues[":target"].(*ddbtypes.AttributeValueMemberSS)
		if !ok {
			return nil, errors.New("fake dynamo changes the invalidation targets only by a string set")
		}
		attribute := in.ExpressionAttributeNames["#targets"]
		held, _ := item[attribute].(*ddbtypes.AttributeValueMemberSS)
		members := []string{}
		if held != nil {
			members = held.Value
		}
		for _, member := range changed.Value {
			switch {
			case strings.HasPrefix(expression, "DELETE"):
				members = slices.DeleteFunc(members, func(m string) bool { return m == member })
			case !slices.Contains(members, member):
				members = append(members, member)
			}
		}
		item[attribute] = &ddbtypes.AttributeValueMemberSS{Value: members}
		f.items[key] = item
		return &dynamodb.UpdateItemOutput{}, nil
	}
	current := int64(0)
	if seq, ok := item["seq"].(*ddbtypes.AttributeValueMemberN); ok {
		current, _ = strconv.ParseInt(seq.Value, 10, 64)
	}
	next := &ddbtypes.AttributeValueMemberN{Value: strconv.FormatInt(current+1, 10)}
	item["seq"] = next
	f.items[key] = item
	return &dynamodb.UpdateItemOutput{Attributes: map[string]ddbtypes.AttributeValue{"seq": next}}, nil
}

func (f *fakeDynamo) Query(_ context.Context, in *dynamodb.QueryInput, _ ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "Query")
	if !aws.ToBool(in.ConsistentRead) {
		return nil, errors.New("fake dynamo answers only consistent reads; the ledger reads what it just wrote")
	}
	pk, _ := in.ExpressionAttributeValues[":pk"].(*ddbtypes.AttributeValueMemberS)
	if pk == nil {
		return nil, fmt.Errorf("fake dynamo needs a partition, got %q", aws.ToString(in.KeyConditionExpression))
	}
	prefix := ""
	switch condition := aws.ToString(in.KeyConditionExpression); condition {
	case "#pk = :pk":
	case "#pk = :pk AND begins_with(#sk, :prefix)":
		value, _ := in.ExpressionAttributeValues[":prefix"].(*ddbtypes.AttributeValueMemberS)
		if value == nil {
			return nil, fmt.Errorf("fake dynamo needs a prefix for %q", condition)
		}
		prefix = value.Value
	default:
		return nil, fmt.Errorf("fake dynamo does not speak the key condition %q", condition)
	}

	after := itemKey(in.ExclusiveStartKey)
	var items []map[string]ddbtypes.AttributeValue
	for _, key := range slices.Sorted(maps.Keys(f.items)) {
		partition, sort, _ := strings.Cut(key, "\x00")
		if partition != pk.Value || !strings.HasPrefix(sort, prefix) || (after != "" && key <= after) {
			continue
		}
		items = append(items, f.items[key])
		if len(items) == f.pageSize {
			last := f.items[key]
			return &dynamodb.QueryOutput{
				Items:            items,
				LastEvaluatedKey: map[string]ddbtypes.AttributeValue{"pk": last["pk"], "sk": last["sk"]},
			}, nil
		}
	}
	return &dynamodb.QueryOutput{Items: items}, nil
}

func promoted(t *testing.T, l *Ledger, id, pointer string, builds map[string]string) {
	t.Helper()
	if err := l.Promote(context.Background(), edge.Promotion{PromotionID: id, Ts: 1, Builds: builds}, pointer); err != nil {
		t.Fatalf("Promote(%s): %v", id, err)
	}
}

func staged(t *testing.T, l *Ledger, app, identity string) {
	t.Helper()
	record := edge.DeploymentRecord{App: app, Identity: identity, EntryFunction: "shop-prod-" + app + "-r1234abcd"}
	if err := l.PutStaged(context.Background(), record); err != nil {
		t.Fatalf("PutStaged(%s/%s): %v", app, identity, err)
	}
}

func historyIDs(t *testing.T, l *Ledger, pointer string) []string {
	t.Helper()
	entries, err := l.History(context.Background(), pointer)
	if err != nil {
		t.Fatalf("History(%q): %v", pointer, err)
	}
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		ids = append(ids, entry.PromotionID)
	}
	return ids
}

func TestLedgerRefusesWhatItCannotReach(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	for name, l := range map[string]*Ledger{
		"no client": {Table: table, Scope: scope},
		"no table":  {Dynamo: newFakeDynamo(), Scope: scope},
		"no scope":  {Dynamo: newFakeDynamo(), Table: table},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if err := l.EnsureSchema(ctx); err == nil {
				t.Error("EnsureSchema succeeded on a ledger that names no store")
			}
			if _, err := l.History(ctx, ""); err == nil {
				t.Error("History succeeded on a ledger that names no store")
			}
		})
	}
}

func TestSchemaVersionTellsAnUnwrittenSchemaFromAnOldOne(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	l := ledgerOn(newFakeDynamo())

	if _, err := l.SchemaVersion(ctx); !errors.Is(err, edge.ErrStoreSchemaUnreadable) {
		t.Errorf("SchemaVersion on an un-migrated partition = %v, want %v so the deploy can say what to re-run", err, edge.ErrStoreSchemaUnreadable)
	}
	if err := l.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	got, err := l.SchemaVersion(ctx)
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if got != edge.StoreSchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", got, edge.StoreSchemaVersion)
	}
}

func TestHistoryIsNewestFirstAndStable(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dynamo := newFakeDynamo()
	l := ledgerOn(dynamo)
	for _, id := range []string{"p1", "p2", "p3"} {
		staged(t, l, "web", id)
		promoted(t, l, id, "", map[string]string{"web": id})
	}

	if got := historyIDs(t, l, ""); !slices.Equal(got, []string{"p3", "p2", "p1"}) {
		t.Errorf("history = %v, want the promotions newest first", got)
	}
	entries, err := l.History(ctx, "")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if !entries[0].Active {
		t.Errorf("history = %v, want the last promotion in effect", entries)
	}
	for _, entry := range entries[1:] {
		if entry.Active {
			t.Errorf("promotion %s reports itself in effect alongside %s", entry.PromotionID, entries[0].PromotionID)
		}
	}
	if got := dynamo.count("Query"); got < 3 {
		t.Errorf("Query calls = %d, want the ledger to have paged through the promotions rather than read one page", got)
	}
}

func TestPromotionsWithOneSequenceKeepAStableOrder(t *testing.T) {
	t.Parallel()

	dynamo := newFakeDynamo()
	l := ledgerOn(dynamo)
	for _, id := range []string{"a", "b", "c"} {
		promoted(t, l, id, "", map[string]string{"web": id})
	}
	for _, item := range dynamo.items {
		if _, ok := item["seq"]; ok {
			item["seq"] = &ddbtypes.AttributeValueMemberN{Value: "1"}
		}
	}

	first := historyIDs(t, l, "")
	if !slices.Equal(first, []string{"c", "b", "a"}) {
		t.Errorf("history = %v, want promotions sharing a sequence ordered by id, newest first", first)
	}
	if second := historyIDs(t, l, ""); !slices.Equal(first, second) {
		t.Errorf("history = %v then %v; the order a reader sees must not move", first, second)
	}
}

func TestPromoteMovesThePointerOnlyFromWhereItWas(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dynamo := newFakeDynamo()
	l := ledgerOn(dynamo)
	staged(t, l, "web", "b1")
	staged(t, l, "web", "b2")
	promoted(t, l, "p1", "", map[string]string{"web": "b1"})

	dynamo.beforePut = func(key string) {
		if !strings.HasSuffix(key, pointerStem+DefaultPointer) {
			return
		}
		dynamo.beforePut = nil
		dynamo.items[key]["promotionId"] = text("racer")
	}

	err := l.Promote(ctx, edge.Promotion{PromotionID: "p2", Ts: 2, Builds: map[string]string{"web": "b2"}}, "")
	if err == nil {
		t.Fatal("a promotion whose pointer moved under it overwrote the deploy that won the race")
	}
	for _, want := range []string{"racer", "p2", "rollback"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, want it to name %q", err, want)
		}
	}
	if got, err := l.pointerAt(ctx, DefaultPointer); err != nil || got != "racer" {
		t.Errorf("pointer = %q (%v), want the promotion that won left in place", got, err)
	}
	if got := historyIDs(t, l, ""); !slices.Equal(got, []string{"p2", "p1"}) {
		t.Errorf("history = %v, want the release the losing deploy did put up recorded but not in effect", got)
	}
}

func TestPromoteRefusesATagAnotherPromotionHolds(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	l := ledgerOn(newFakeDynamo())
	if err := l.Promote(ctx, edge.Promotion{PromotionID: "p1", Ts: 1, Tag: "v1", Builds: map[string]string{"web": "b1"}}, ""); err != nil {
		t.Fatalf("Promote(p1): %v", err)
	}

	err := l.Promote(ctx, edge.Promotion{PromotionID: "p2", Ts: 2, Tag: "v1", Builds: map[string]string{"web": "b2"}}, "")
	if err == nil {
		t.Fatal("a second promotion took a tag the first holds; `ocel rollback --tag v1` would be ambiguous")
	}
	if !strings.Contains(err.Error(), "p1") || !strings.Contains(err.Error(), "v1") {
		t.Errorf("error = %v, want it to name the tag and what already holds it", err)
	}
	if got := historyIDs(t, l, ""); !slices.Equal(got, []string{"p1"}) {
		t.Errorf("history = %v, want the refused promotion left unrecorded", got)
	}

	if err := l.Promote(ctx, edge.Promotion{PromotionID: "p1", Ts: 3, Tag: "v1", Builds: map[string]string{"web": "b1"}}, ""); err != nil {
		t.Errorf("re-promoting the release that already holds the tag: %v", err)
	}
}

func TestPruneSortsEveryPromotionIntoOneSide(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	l := ledgerOn(newFakeDynamo())
	ids := []string{"p1", "p2", "p3", "p4"}
	for _, id := range ids {
		staged(t, l, "web", id)
		promoted(t, l, id, "", map[string]string{"web": id})
	}

	result, err := l.Prune(ctx, 2, "")
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if !slices.Equal(result.KeptPromotionIDs, []string{"p4", "p3"}) {
		t.Errorf("KeptPromotionIDs = %v, want the newest two", result.KeptPromotionIDs)
	}
	for _, id := range ids {
		kept := slices.Contains(result.KeptPromotionIDs, id)
		removed := slices.Contains(result.RemovedPromotionIDs, id)
		if kept == removed {
			t.Errorf("promotion %q is reported kept=%v removed=%v, want exactly one", id, kept, removed)
		}
	}
	for _, key := range result.RemovedRecordKeys {
		if slices.Contains(result.SurvivingRecordKeys, key) {
			t.Errorf("record key %q is reported both removed and surviving", key)
		}
	}
	if !slices.Equal(result.SurvivingRecordKeys, []string{RecordKey("web", "p3"), RecordKey("web", "p4")}) {
		t.Errorf("SurvivingRecordKeys = %v, want the records the kept promotions name", result.SurvivingRecordKeys)
	}
	if got := historyIDs(t, l, ""); !slices.Equal(got, []string{"p4", "p3"}) {
		t.Errorf("history after the prune = %v, want the kept window", got)
	}
}

func TestPruneKeepsTheReleaseInEffect(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	l := ledgerOn(newFakeDynamo())
	for _, id := range []string{"p1", "p2", "p3"} {
		promoted(t, l, id, "", map[string]string{"web": id})
	}
	promoted(t, l, "p1", "", map[string]string{"web": "p1"})

	result, err := l.Prune(ctx, 1, "")
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if !slices.Contains(result.KeptPromotionIDs, "p1") {
		t.Errorf("KeptPromotionIDs = %v, want the promotion in effect among them", result.KeptPromotionIDs)
	}
	if slices.Contains(result.RemovedPromotionIDs, "p1") {
		t.Errorf("RemovedPromotionIDs = %v, want the release being served left alone", result.RemovedPromotionIDs)
	}
}

func TestRemovePointerTakesItsOwnAndLeavesTheRest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	l := ledgerOn(newFakeDynamo())
	staged(t, l, "web", "b1")
	staged(t, l, "web", "b2")
	promoted(t, l, "held", "", map[string]string{"web": "b1"})
	promoted(t, l, "pointed", "pr-7", map[string]string{"web": "b2"})

	result, err := l.RemovePointer(ctx, "pr-7")
	if err != nil {
		t.Fatalf("RemovePointer: %v", err)
	}
	if !slices.Equal(result.RemovedPromotionIDs, []string{"pointed"}) {
		t.Errorf("RemovedPromotionIDs = %v, want only the pointer's promotion", result.RemovedPromotionIDs)
	}
	for _, key := range result.RemovedRecordKeys {
		if slices.Contains(result.SurvivingRecordKeys, key) {
			t.Errorf("record key %q is reported both removed and surviving", key)
		}
	}
	if got := historyIDs(t, l, "pr-7"); len(got) != 0 {
		t.Errorf("history under the removed pointer = %v, want nothing", got)
	}
	if got := historyIDs(t, l, ""); !slices.Equal(got, []string{"held"}) {
		t.Errorf("history outside the pointer = %v, want what the pointer never held", got)
	}
	if got, err := l.Pointers(ctx); err != nil || !slices.Equal(got, []string{DefaultPointer}) {
		t.Errorf("Pointers = %v (%v), want the pointer forgotten", got, err)
	}
}

func TestDropsBatchAndRetryWhatDynamoDeferred(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dynamo := newFakeDynamo()
	l := ledgerOn(dynamo)
	for i := range 30 {
		id := fmt.Sprintf("p%02d", i)
		staged(t, l, "web", id)
		promoted(t, l, id, "", map[string]string{"web": id})
	}
	dynamo.calls = nil
	dynamo.defer1st = true

	result, err := l.Prune(ctx, 1, "")
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if len(result.RemovedPromotionIDs) != 29 {
		t.Fatalf("RemovedPromotionIDs = %d, want the 29 outside a window of one", len(result.RemovedPromotionIDs))
	}
	if got := dynamo.count("DeleteItem"); got != 0 {
		t.Errorf("DeleteItem calls = %d, want the deletes batched", got)
	}
	if got := dynamo.count("BatchWriteItem"); got > 5 {
		t.Errorf("BatchWriteItem calls = %d for 58 deletes, want them batched 25 at a time", got)
	}
	for _, sk := range dynamo.sortKeys() {
		for _, id := range result.RemovedPromotionIDs {
			if strings.HasSuffix(sk, "#"+id) {
				t.Errorf("%q survived the prune that reported %q removed; a deferred write was never retried", sk, id)
			}
		}
	}
}

func TestDropTakesTheRowItReports(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dynamo := newFakeDynamo()
	l := ledgerOn(dynamo)
	staged(t, l, "web", "b1")
	staged(t, l, "web", "b2")
	promoted(t, l, "p1", "", map[string]string{"web": "b1"})
	promoted(t, l, "p2", "", map[string]string{"web": "b2"})

	row := dynamo.items[partition+"#"+scope+"\x00"+promotionStem+DefaultPointer+"#p1"]
	delete(row, "promotionId")

	result, err := l.Prune(ctx, 1, "")
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if !slices.Equal(result.RemovedPromotionIDs, []string{"p1"}) {
		t.Fatalf("RemovedPromotionIDs = %v, want the row outside the window named by its key", result.RemovedPromotionIDs)
	}
	for _, sk := range dynamo.sortKeys() {
		if strings.HasPrefix(sk, promotionStem) && strings.HasSuffix(sk, "#p1") {
			t.Errorf("%q survived a prune that reported it removed", sk)
		}
		if sk == promotionStem+DefaultPointer+"#" {
			t.Errorf("the prune deleted %q, a key no promotion ever had", sk)
		}
	}
}

func TestDestroyErasesTheWholeScope(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dynamo := newFakeDynamo()
	l := ledgerOn(dynamo)
	if err := l.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	staged(t, l, "web", "b1")
	promoted(t, l, "p1", "", map[string]string{"web": "b1"})

	other := &Ledger{Dynamo: dynamo, Table: table, Scope: "production/other"}
	if err := other.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema(other): %v", err)
	}

	if err := l.Destroy(ctx); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	for key := range dynamo.items {
		if strings.HasPrefix(key, partition+"#"+scope+"\x00") {
			t.Errorf("%q survived the destroy", key)
		}
	}
	if _, err := other.SchemaVersion(ctx); err != nil {
		t.Errorf("destroying one project's ledger took another's: %v", err)
	}
}

func TestRecordRoundTripsWhatTheDeployStaged(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	l := ledgerOn(newFakeDynamo())
	staged(t, l, "web", "d1.f1")

	record, found, err := l.Record(ctx, "web", "d1.f1")
	if err != nil || !found {
		t.Fatalf("Record: %v (found %v)", err, found)
	}
	if record.EntryFunction != "shop-prod-web-r1234abcd" {
		t.Errorf("EntryFunction = %q, want what the deploy staged", record.EntryFunction)
	}
	if _, found, err := l.Record(ctx, "web", "never"); err != nil || found {
		t.Errorf("Record of an identity nothing staged = found %v (%v), want nothing", found, err)
	}
	if err := l.PutStaged(ctx, edge.DeploymentRecord{App: "web"}); err == nil {
		t.Error("PutStaged accepted a record with no identity; the ledger keys records by both")
	}
}

func targetsHeld(t *testing.T, dynamo *fakeDynamo, ledgerScope string) []string {
	t.Helper()
	item, ok := dynamo.items[partition+"#"+ledgerScope+"\x00"+invalidationKey]
	if !ok {
		return nil
	}
	held, ok := item[invalidationTargetsAttribute].(*ddbtypes.AttributeValueMemberSS)
	if !ok {
		t.Fatalf("the invalidation targets of %s are not a string set: %#v", ledgerScope, item[invalidationTargetsAttribute])
	}
	return held.Value
}

func TestNoteInvalidationTargetGathersEveryFrontOnce(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dynamo := newFakeDynamo()
	l := ledgerOn(dynamo)

	for _, distribution := range []string{"E1PROD", "E2WILD", "E1PROD"} {
		if err := l.NoteInvalidationTarget(ctx, distribution); err != nil {
			t.Fatalf("NoteInvalidationTarget(%s): %v", distribution, err)
		}
	}
	if got := targetsHeld(t, dynamo, scope); !slices.Equal(got, []string{"E1PROD", "E2WILD"}) {
		t.Errorf("targets = %v, want each front once", got)
	}

	other := &Ledger{Dynamo: dynamo, Table: table, Scope: "production/other"}
	if err := other.NoteInvalidationTarget(ctx, "E3OTHER"); err != nil {
		t.Fatalf("NoteInvalidationTarget(other): %v", err)
	}
	if got := targetsHeld(t, dynamo, scope); !slices.Equal(got, []string{"E1PROD", "E2WILD"}) {
		t.Errorf("targets = %v after another project noted a front of its own", got)
	}

	if err := l.NoteInvalidationTarget(ctx, ""); err == nil {
		t.Error("NoteInvalidationTarget accepted a front with no id")
	}
}

func TestDestroyForgetsTheInvalidationTargets(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dynamo := newFakeDynamo()
	l := ledgerOn(dynamo)
	if err := l.NoteInvalidationTarget(ctx, "E1PROD"); err != nil {
		t.Fatalf("NoteInvalidationTarget: %v", err)
	}
	if err := l.Destroy(ctx); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if got := targetsHeld(t, dynamo, scope); got != nil {
		t.Errorf("targets = %v, want a destroyed scope to invalidate nothing", got)
	}
}

func TestForgetInvalidationTargetLeavesTheLiveFrontsAlone(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dynamo := newFakeDynamo()
	l := ledgerOn(dynamo)

	for _, distribution := range []string{"E1PROD", "E2SECOND"} {
		if err := l.NoteInvalidationTarget(ctx, distribution); err != nil {
			t.Fatalf("NoteInvalidationTarget(%s): %v", distribution, err)
		}
	}
	if err := l.ForgetInvalidationTarget(ctx, "E1PROD"); err != nil {
		t.Fatalf("ForgetInvalidationTarget: %v", err)
	}
	if got := targetsHeld(t, dynamo, scope); !slices.Equal(got, []string{"E2SECOND"}) {
		t.Errorf("targets = %v, want only the front this project still serves from", got)
	}
	if err := l.ForgetInvalidationTarget(ctx, ""); err == nil {
		t.Error("ForgetInvalidationTarget accepted a front with no id")
	}
}

func TestSubstrateScopeHoldsTheFrontsEveryProjectShares(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dynamo := newFakeDynamo()
	substrate := &Ledger{Dynamo: dynamo, Table: table, Scope: Scope(edge.ClassPreview, "")}

	if err := substrate.NoteInvalidationTarget(ctx, "EWILDCARD"); err != nil {
		t.Fatalf("NoteInvalidationTarget: %v", err)
	}
	if got := targetsHeld(t, dynamo, string(edge.ClassPreview)); !slices.Equal(got, []string{"EWILDCARD"}) {
		t.Errorf("substrate targets = %v, want the wildcard every preview is served from", got)
	}
	if got := targetsHeld(t, dynamo, scope); got != nil {
		t.Errorf("project targets = %v, want a substrate-wide front recorded once rather than per project", got)
	}
}

func TestTheInvalidatorReadsTheKeysThisLedgerWrites(t *testing.T) {
	t.Parallel()

	const targets = "../../functions/tag-invalidator/src/targets.mts"
	source, err := os.ReadFile(targets)
	if err != nil {
		t.Fatalf("read the invalidator's view of the ledger: %v", err)
	}
	for what, want := range map[string]string{
		"targetsSortKey":   invalidationKey,
		"targetsAttribute": invalidationTargetsAttribute,
		"partitionPrefix":  partition + "#",
	} {
		named := regexp.MustCompile(what + ` = "([^"]+)"`).FindSubmatch(source)
		if named == nil {
			t.Fatalf("%s no longer names %s", targets, what)
		}
		if got := string(named[1]); got != want {
			t.Errorf("the invalidator reads %s %q and this ledger writes %q, so every raise reads an item nobody wrote", what, got, want)
		}
	}
}

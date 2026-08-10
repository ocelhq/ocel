package vars

import (
	"context"
	"errors"
	"maps"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

func TestSetRoundTripsAndLeavesNoPlaintextAtRest(t *testing.T) {
	store, ddb, crypto := newTestStore(t)
	c := testCoordinate()

	if _, err := store.Set(context.Background(), c, "sk_live_secret", nil); err != nil {
		t.Fatalf("Set err = %v", err)
	}

	got, err := store.Get(context.Background(), c, true)
	if err != nil {
		t.Fatalf("Get err = %v", err)
	}
	if got.Plaintext != "sk_live_secret" {
		t.Errorf("Get plaintext = %q, want %q", got.Plaintext, "sk_live_secret")
	}
	if got.Version != 1 {
		t.Errorf("Get version = %d, want 1", got.Version)
	}
	if got.Size != int64(len("sk_live_secret")) {
		t.Errorf("Get size = %d, want %d", got.Size, len("sk_live_secret"))
	}
	if crypto.encrypts != 1 {
		t.Errorf("encrypt calls = %d, want 1", crypto.encrypts)
	}
	if len(crypto.keyIDs) != 1 || crypto.keyIDs[0] != store.KeyARN {
		t.Errorf("encrypted under %v, want %q", crypto.keyIDs, store.KeyARN)
	}

	for pk, sks := range ddb.items {
		for sk, item := range sks {
			for name, attr := range item {
				if s, ok := attr.(*ddbtypes.AttributeValueMemberS); ok && strings.Contains(s.Value, "sk_live_secret") {
					t.Fatalf("plaintext at rest in %s/%s attribute %q", pk, sk, name)
				}
			}
			if b := binaryAttr(item, "ciphertext"); len(b) == 0 {
				t.Fatalf("item %s/%s carries no ciphertext", pk, sk)
			}
		}
	}
}

func TestSetRejectsAWriteAgainstAStaleVersion(t *testing.T) {
	store, _, _ := newTestStore(t)
	c := testCoordinate()

	if _, err := store.Set(context.Background(), c, "first", nil); err != nil {
		t.Fatalf("Set err = %v", err)
	}
	if _, err := store.Set(context.Background(), c, "second", nil); err != nil {
		t.Fatalf("Set err = %v", err)
	}

	stale := int64(1)
	_, err := store.Set(context.Background(), c, "third", &stale)
	if !errors.Is(err, ErrStaleVersion) {
		t.Fatalf("Set with stale expected_version err = %v, want ErrStaleVersion", err)
	}

	got, err := store.Get(context.Background(), c, true)
	if err != nil {
		t.Fatalf("Get err = %v", err)
	}
	if got.Plaintext != "second" {
		t.Errorf("value after rejected write = %q, want the write to have been rejected rather than applied", got.Plaintext)
	}
}

func TestSetRejectsAConcurrentWriteThatLostTheRace(t *testing.T) {
	store, ddb, _ := newTestStore(t)
	c := testCoordinate()

	if _, err := store.Set(context.Background(), c, "first", nil); err != nil {
		t.Fatalf("Set err = %v", err)
	}

	seen := int64(1)
	if _, err := store.Set(context.Background(), c, "committed by the other writer", &seen); err != nil {
		t.Fatalf("Set err = %v", err)
	}
	_, err := store.Set(context.Background(), c, "lost the race", &seen)
	if !errors.Is(err, ErrStaleVersion) {
		t.Fatalf("racing Set err = %v, want ErrStaleVersion", err)
	}

	item, ok := ddb.get(PartitionKey(c.Slug, store.Class), currentSortKey(c.canonical()))
	if !ok {
		t.Fatal("current pointer missing")
	}
	if got := numberAttr(item, "version"); got != 2 {
		t.Errorf("version after the losing write = %d, want 2", got)
	}
}

func TestSetRejectsAWriterOvertakenBetweenItsReadAndItsCommit(t *testing.T) {
	store, ddb, _ := newTestStore(t)
	c := testCoordinate()

	if _, err := store.Set(context.Background(), c, "first", nil); err != nil {
		t.Fatalf("Set err = %v", err)
	}
	ddb.beforeTransact = func() {
		if _, err := store.Set(context.Background(), c, "the other writer", nil); err != nil {
			t.Errorf("competing Set err = %v", err)
		}
	}

	if _, err := store.Set(context.Background(), c, "overtaken", nil); !errors.Is(err, ErrStaleVersion) {
		t.Fatalf("overtaken Set err = %v, want ErrStaleVersion", err)
	}

	got, err := store.Get(context.Background(), c, true)
	if err != nil {
		t.Fatalf("Get err = %v", err)
	}
	if got.Plaintext != "the other writer" {
		t.Errorf("value = %q, want the overtaken write rejected rather than applied", got.Plaintext)
	}
}

func TestSetRequiresAbsenceWhenExpectedVersionIsZero(t *testing.T) {
	store, _, _ := newTestStore(t)
	c := testCoordinate()

	create := int64(0)
	if _, err := store.Set(context.Background(), c, "first", &create); err != nil {
		t.Fatalf("creating Set err = %v", err)
	}
	_, err := store.Set(context.Background(), c, "second", &create)
	if !errors.Is(err, ErrStaleVersion) {
		t.Fatalf("second creating Set err = %v, want ErrStaleVersion", err)
	}
}

func TestSetEmitsTheOptimisticCondition(t *testing.T) {
	store, ddb, _ := newTestStore(t)

	if _, err := store.Set(context.Background(), testCoordinate(), "v", nil); err != nil {
		t.Fatalf("Set err = %v", err)
	}

	var conditions []string
	for _, w := range ddb.transactions[0] {
		if w.Put != nil && w.Put.ConditionExpression != nil {
			conditions = append(conditions, *w.Put.ConditionExpression)
		}
	}
	want := "attribute_not_exists(pk) OR #version = :seen"
	if len(conditions) != 1 || conditions[0] != want {
		t.Errorf("transaction conditions = %v, want exactly one %q", conditions, want)
	}
}

func TestSetPrunesTheVersionOutsideTheWindow(t *testing.T) {
	store, ddb, _ := newTestStore(t)
	c := testCoordinate()

	for i := 0; i < historyWindow; i++ {
		if _, err := store.Set(context.Background(), c, "v", nil); err != nil {
			t.Fatalf("Set %d err = %v", i, err)
		}
	}
	if got := deletesIn(ddb.transactions); got != 0 {
		t.Fatalf("history deletes within the window = %d, want 0", got)
	}

	if _, err := store.Set(context.Background(), c, "v", nil); err != nil {
		t.Fatalf("Set err = %v", err)
	}

	last := ddb.transactions[len(ddb.transactions)-1]
	var deleted []string
	for _, w := range last {
		if w.Delete != nil {
			deleted = append(deleted, stringAttr(w.Delete.Key, "sk"))
		}
	}
	want := historySortKey(c.canonical(), 1)
	if len(deleted) != 1 || deleted[0] != want {
		t.Fatalf("pruned %v, want exactly %q", deleted, want)
	}

	for _, q := range ddb.queries {
		t.Fatalf("Set queried the table (%v); the pruned version must be computed", q.KeyConditionExpression)
	}

	versions, err := store.Versions(context.Background(), c)
	if err != nil {
		t.Fatalf("Versions err = %v", err)
	}
	if len(versions) != historyWindow {
		t.Errorf("history depth = %d, want %d", len(versions), historyWindow)
	}
}

func TestVersionsReadsNewestFirst(t *testing.T) {
	store, _, _ := newTestStore(t)
	c := testCoordinate()

	for _, v := range []string{"o", "tw", "three"} {
		if _, err := store.Set(context.Background(), c, v, nil); err != nil {
			t.Fatalf("Set %q err = %v", v, err)
		}
	}

	versions, err := store.Versions(context.Background(), c)
	if err != nil {
		t.Fatalf("Versions err = %v", err)
	}
	if len(versions) != 3 {
		t.Fatalf("got %d versions, want 3", len(versions))
	}
	wantNumbers := []int64{3, 2, 1}
	wantSizes := []int64{5, 2, 1}
	for i, v := range versions {
		if v.Version != wantNumbers[i] {
			t.Errorf("versions[%d].Version = %d, want %d", i, v.Version, wantNumbers[i])
		}
		if v.Size != wantSizes[i] {
			t.Errorf("versions[%d].Size = %d, want %d", i, v.Size, wantSizes[i])
		}
		if i > 0 && v.CreatedAt >= versions[i-1].CreatedAt {
			t.Errorf("versions[%d].CreatedAt = %d, want older than versions[%d]'s %d", i, v.CreatedAt, i-1, versions[i-1].CreatedAt)
		}
	}
}

func TestVersionsNeverDecrypts(t *testing.T) {
	store, _, crypto := newTestStore(t)
	c := testCoordinate()

	for _, v := range []string{"sk_leaked", "sk_rotated"} {
		if _, err := store.Set(context.Background(), c, v, nil); err != nil {
			t.Fatalf("Set %q err = %v", v, err)
		}
	}
	crypto.decrypts = 0

	versions, err := store.Versions(context.Background(), c)
	if err != nil {
		t.Fatalf("Versions err = %v", err)
	}
	if len(versions) != 2 {
		t.Fatalf("got %d versions, want 2", len(versions))
	}
	if crypto.decrypts != 0 {
		t.Errorf("decrypt calls = %d, want 0: history never opens a ciphertext", crypto.decrypts)
	}
}

func TestGetWithoutRevealNeverDecrypts(t *testing.T) {
	store, _, crypto := newTestStore(t)
	c := testCoordinate()

	if _, err := store.Set(context.Background(), c, "sk_live_secret", nil); err != nil {
		t.Fatalf("Set err = %v", err)
	}
	crypto.decrypts = 0

	got, err := store.Get(context.Background(), c, false)
	if err != nil {
		t.Fatalf("Get err = %v", err)
	}
	if crypto.decrypts != 0 {
		t.Errorf("decrypt calls = %d, want 0 without reveal", crypto.decrypts)
	}
	if got.Plaintext != "" {
		t.Errorf("Get plaintext = %q, want it withheld without reveal", got.Plaintext)
	}
	if got.Version != 1 || got.Size == 0 {
		t.Errorf("Get metadata = %+v, want version and size reported without the value", got.Metadata)
	}
}

func TestGetReportsAnUnsetCoordinate(t *testing.T) {
	store, _, _ := newTestStore(t)
	if _, err := store.Get(context.Background(), testCoordinate(), true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get on an unset coordinate err = %v, want ErrNotFound", err)
	}
}

func TestDeleteUnsetsTheValueAndKeepsHistory(t *testing.T) {
	store, _, _ := newTestStore(t)
	c := testCoordinate()

	if _, err := store.Set(context.Background(), c, "one", nil); err != nil {
		t.Fatalf("Set err = %v", err)
	}
	if _, err := store.Set(context.Background(), c, "two", nil); err != nil {
		t.Fatalf("Set err = %v", err)
	}

	deleted, err := store.Delete(context.Background(), c, nil)
	if err != nil {
		t.Fatalf("Delete err = %v", err)
	}
	if !deleted {
		t.Error("Delete reported nothing deleted, want it to report the pointer removed")
	}
	if _, err := store.Get(context.Background(), c, false); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get after Delete err = %v, want ErrNotFound", err)
	}

	versions, err := store.Versions(context.Background(), c)
	if err != nil {
		t.Fatalf("Versions err = %v", err)
	}
	if len(versions) != 2 {
		t.Errorf("history after Delete = %d entries, want 2 kept", len(versions))
	}
}

func TestDeleteRejectsADeleteAgainstAStaleVersion(t *testing.T) {
	store, _, _ := newTestStore(t)
	c := testCoordinate()

	if _, err := store.Set(context.Background(), c, "first", nil); err != nil {
		t.Fatalf("Set err = %v", err)
	}
	if _, err := store.Set(context.Background(), c, "second", nil); err != nil {
		t.Fatalf("Set err = %v", err)
	}

	stale := int64(1)
	deleted, err := store.Delete(context.Background(), c, &stale)
	if !errors.Is(err, ErrStaleVersion) {
		t.Fatalf("Delete against a stale expected version err = %v, want ErrStaleVersion", err)
	}
	if deleted {
		t.Error("Delete reported a deletion it refused to make")
	}
	got, err := store.Get(context.Background(), c, true)
	if err != nil {
		t.Fatalf("Get err = %v", err)
	}
	if got.Plaintext != "second" {
		t.Errorf("value after the rejected delete = %q, want %q — a refused delete must not be applied", got.Plaintext, "second")
	}

	current := int64(2)
	if deleted, err := store.Delete(context.Background(), c, &current); err != nil || !deleted {
		t.Fatalf("Delete quoting the current version = (%v, %v), want it to land", deleted, err)
	}
	if _, err := store.Get(context.Background(), c, false); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get after the honoured delete err = %v, want ErrNotFound", err)
	}
}

func TestDeleteRejectsADeleterOvertakenBetweenItsReadAndItsCommit(t *testing.T) {
	store, ddb, _ := newTestStore(t)
	c := testCoordinate()

	if _, err := store.Set(context.Background(), c, "first", nil); err != nil {
		t.Fatalf("Set err = %v", err)
	}
	ddb.beforePut = func() {
		if _, err := store.Set(context.Background(), c, "committed by the other writer", nil); err != nil {
			t.Fatalf("competing Set err = %v", err)
		}
	}

	if _, err := store.Delete(context.Background(), c, nil); !errors.Is(err, ErrStaleVersion) {
		t.Fatalf("overtaken Delete err = %v, want ErrStaleVersion", err)
	}
	got, err := store.Get(context.Background(), c, true)
	if err != nil {
		t.Fatalf("Get err = %v", err)
	}
	if got.Plaintext != "committed by the other writer" {
		t.Errorf("value after the overtaken delete = %q, want the write that got there first", got.Plaintext)
	}
}

func TestDeleteExpectingNoValueHoldsOnlyWhileTheCellIsUnset(t *testing.T) {
	store, _, _ := newTestStore(t)
	c := testCoordinate()
	unset := int64(0)

	if _, err := store.Set(context.Background(), c, "first", nil); err != nil {
		t.Fatalf("Set err = %v", err)
	}
	if _, err := store.Delete(context.Background(), c, &unset); !errors.Is(err, ErrStaleVersion) {
		t.Fatalf("Delete expecting no value on a set cell err = %v, want ErrStaleVersion", err)
	}

	if _, err := store.Delete(context.Background(), c, nil); err != nil {
		t.Fatalf("Delete err = %v", err)
	}
	deleted, err := store.Delete(context.Background(), c, &unset)
	if err != nil {
		t.Fatalf("repeated Delete expecting no value err = %v, want an idempotent no-op", err)
	}
	if deleted {
		t.Error("the repeated Delete reported a deletion, want none — there was nothing left to unset")
	}
}

func TestDeleteEmitsTheOptimisticCondition(t *testing.T) {
	store, ddb, _ := newTestStore(t)
	c := testCoordinate()

	if _, err := store.Set(context.Background(), c, "v", nil); err != nil {
		t.Fatalf("Set err = %v", err)
	}
	if _, err := store.Delete(context.Background(), c, nil); err != nil {
		t.Fatalf("Delete err = %v", err)
	}

	if len(ddb.puts) != 1 {
		t.Fatalf("Delete emitted %d point writes, want exactly the tombstone", len(ddb.puts))
	}
	want := "attribute_not_exists(pk) OR #version = :seen"
	if got := aws.ToString(ddb.puts[0].ConditionExpression); got != want {
		t.Errorf("tombstone condition = %q, want %q", got, want)
	}
	if got := numberAttr(ddb.puts[0].ExpressionAttributeValues, ":seen"); got != 1 {
		t.Errorf("tombstone conditions on version %d, want 1 — the version the delete read", got)
	}
}

func TestSetAfterDeleteContinuesTheVersionSequence(t *testing.T) {
	store, _, _ := newTestStore(t)
	c := testCoordinate()

	for _, v := range []string{"o", "tw"} {
		if _, err := store.Set(context.Background(), c, v, nil); err != nil {
			t.Fatalf("Set %q err = %v", v, err)
		}
	}
	if _, err := store.Delete(context.Background(), c, nil); err != nil {
		t.Fatalf("Delete err = %v", err)
	}

	written, err := store.Set(context.Background(), c, "three", nil)
	if err != nil {
		t.Fatalf("Set after Delete err = %v", err)
	}
	if written.Version != 3 {
		t.Errorf("version written after a delete = %d, want 3", written.Version)
	}

	got, err := store.Get(context.Background(), c, true)
	if err != nil {
		t.Fatalf("Get err = %v", err)
	}
	if got.Plaintext != "three" || got.Version != 3 {
		t.Errorf("Get after the rewrite = %q at version %d, want %q at 3", got.Plaintext, got.Version, "three")
	}

	versions, err := store.Versions(context.Background(), c)
	if err != nil {
		t.Fatalf("Versions err = %v", err)
	}
	wantNumbers := []int64{3, 2, 1}
	wantSizes := []int64{5, 2, 1}
	if len(versions) != len(wantNumbers) {
		t.Fatalf("history depth = %d, want %d; the write after a delete overwrote an existing version", len(versions), len(wantNumbers))
	}
	for i, v := range versions {
		if v.Version != wantNumbers[i] || v.Size != wantSizes[i] {
			t.Errorf("versions[%d] = %d/%d bytes, want %d/%d", i, v.Version, v.Size, wantNumbers[i], wantSizes[i])
		}
	}
}

func TestHistoryStaysCappedAcrossADelete(t *testing.T) {
	store, _, _ := newTestStore(t)
	c := testCoordinate()

	for i := 0; i < historyWindow; i++ {
		if _, err := store.Set(context.Background(), c, "v", nil); err != nil {
			t.Fatalf("Set %d err = %v", i, err)
		}
	}
	if _, err := store.Delete(context.Background(), c, nil); err != nil {
		t.Fatalf("Delete err = %v", err)
	}
	if _, err := store.Set(context.Background(), c, "after the delete", nil); err != nil {
		t.Fatalf("Set after Delete err = %v", err)
	}

	versions, err := store.Versions(context.Background(), c)
	if err != nil {
		t.Fatalf("Versions err = %v", err)
	}
	if len(versions) != historyWindow {
		t.Errorf("history depth = %d, want %d", len(versions), historyWindow)
	}
	if versions[0].Version != historyWindow+1 {
		t.Errorf("newest version = %d, want %d", versions[0].Version, historyWindow+1)
	}
	if got := versions[len(versions)-1].Version; got != 2 {
		t.Errorf("oldest version kept = %d, want 2", got)
	}
}

func TestSetWithExpectedVersionZeroSucceedsAfterADelete(t *testing.T) {
	store, _, _ := newTestStore(t)
	c := testCoordinate()

	if _, err := store.Set(context.Background(), c, "first", nil); err != nil {
		t.Fatalf("Set err = %v", err)
	}
	if _, err := store.Delete(context.Background(), c, nil); err != nil {
		t.Fatalf("Delete err = %v", err)
	}

	create := int64(0)
	written, err := store.Set(context.Background(), c, "recreated", &create)
	if err != nil {
		t.Fatalf("creating Set after Delete err = %v, want it to succeed on an unset cell", err)
	}
	if written.Version != 2 {
		t.Errorf("version after the recreate = %d, want 2", written.Version)
	}
}

func TestDeleteLeavesNoValueAtRest(t *testing.T) {
	store, ddb, _ := newTestStore(t)
	c := testCoordinate()

	if _, err := store.Set(context.Background(), c, "sk_live_secret", nil); err != nil {
		t.Fatalf("Set err = %v", err)
	}
	if _, err := store.Delete(context.Background(), c, nil); err != nil {
		t.Fatalf("Delete err = %v", err)
	}

	current, ok := ddb.get(PartitionKey(c.Slug, store.Class), currentSortKey(c.canonical()))
	if !ok {
		return
	}
	if len(binaryAttr(current, "ciphertext")) != 0 {
		t.Error("the deleted cell still carries its ciphertext")
	}
}

func TestListOmitsDeletedCells(t *testing.T) {
	store, _, _ := newTestStore(t)
	kept := Coordinate{Slug: "shop", Key: "POSTHOG_ID"}
	removed := testCoordinate()

	for _, c := range []Coordinate{kept, removed} {
		if _, err := store.Set(context.Background(), c, "v", nil); err != nil {
			t.Fatalf("Set %+v err = %v", c, err)
		}
	}
	if _, err := store.Delete(context.Background(), removed, nil); err != nil {
		t.Fatalf("Delete err = %v", err)
	}

	got, err := store.List(context.Background(), "shop")
	if err != nil {
		t.Fatalf("List err = %v", err)
	}
	if len(got) != 1 || got[0].Coordinate != kept {
		t.Errorf("List = %+v, want only %+v", got, kept)
	}
}

func TestDeleteIsIdempotent(t *testing.T) {
	store, _, _ := newTestStore(t)
	deleted, err := store.Delete(context.Background(), testCoordinate(), nil)
	if err != nil {
		t.Fatalf("Delete err = %v", err)
	}
	if deleted {
		t.Error("Delete on an unset coordinate reported a deletion")
	}
}

func TestListReturnsCurrentValuesWithoutHistoryOrPlaintext(t *testing.T) {
	store, _, crypto := newTestStore(t)

	cells := []Coordinate{
		{Slug: "shop", Key: "STRIPE_API_KEY"},
		{Slug: "shop", Key: "POSTHOG_ID", Folder: "/web"},
		{Slug: "shop", Key: "POSTHOG_ID", Folder: "/admin"},
		{Slug: "shop", Key: "STRIPE_API_KEY", Environment: "staging"},
	}
	for _, c := range cells {
		for i := 0; i < 3; i++ {
			if _, err := store.Set(context.Background(), c, "value-of-"+c.Key, nil); err != nil {
				t.Fatalf("Set %+v err = %v", c, err)
			}
		}
	}
	if _, err := store.Set(context.Background(), Coordinate{Slug: "other", Key: "STRIPE_API_KEY"}, "elsewhere", nil); err != nil {
		t.Fatalf("Set err = %v", err)
	}
	crypto.decrypts = 0

	got, err := store.List(context.Background(), "shop")
	if err != nil {
		t.Fatalf("List err = %v", err)
	}
	if len(got) != len(cells) {
		t.Fatalf("List returned %d cells, want %d (history must not be dragged along)", len(got), len(cells))
	}
	if crypto.decrypts != 0 {
		t.Errorf("List decrypted %d times, want 0", crypto.decrypts)
	}
	for _, m := range got {
		if m.Version != 3 {
			t.Errorf("List %+v version = %d, want the current 3", m.Coordinate, m.Version)
		}
		if m.Coordinate.Slug != "shop" {
			t.Errorf("List returned a cell from another project: %+v", m.Coordinate)
		}
	}
	if !containsCoordinate(got, Coordinate{Slug: "shop", Folder: "/admin", Key: "POSTHOG_ID", Environment: ""}) {
		t.Errorf("List = %+v, want it to carry the folder-scoped cell's coordinate", got)
	}
	if !containsCoordinate(got, Coordinate{Slug: "shop", Folder: "", Key: "STRIPE_API_KEY", Environment: "staging"}) {
		t.Errorf("List = %+v, want it to carry the named-environment cell's coordinate", got)
	}
}

func TestSetBindsTheCiphertextToItsCoordinate(t *testing.T) {
	store, _, crypto := newTestStore(t)
	c := Coordinate{Slug: "shop", Folder: "/web", Key: "STRIPE_API_KEY", Environment: "staging"}

	if _, err := store.Set(context.Background(), c, "sk_live_secret", nil); err != nil {
		t.Fatalf("Set err = %v", err)
	}

	want := map[string]string{
		"slug":        "shop",
		"folder":      "/web",
		"key":         "STRIPE_API_KEY",
		"environment": "staging",
	}
	if len(crypto.contexts) != 1 || !maps.Equal(crypto.contexts[0], want) {
		t.Fatalf("encryption context = %v, want exactly one %v", crypto.contexts, want)
	}
}

func TestTheEncryptionContextNamesEveryComponentOfARootCell(t *testing.T) {
	store, _, crypto := newTestStore(t)

	if _, err := store.Set(context.Background(), testCoordinate(), "v", nil); err != nil {
		t.Fatalf("Set err = %v", err)
	}

	want := map[string]string{
		"slug":        "shop",
		"folder":      rootFolder,
		"key":         "STRIPE_API_KEY",
		"environment": classWideEnvironment,
	}
	if len(crypto.contexts) != 1 || !maps.Equal(crypto.contexts[0], want) {
		t.Fatalf("encryption context = %v, want exactly one %v", crypto.contexts, want)
	}
}

func TestARelocatedCiphertextDoesNotDecrypt(t *testing.T) {
	origin := Coordinate{Slug: "shop", Key: "STRIPE_API_KEY"}
	for name, elsewhere := range map[string]Coordinate{
		"a neighbouring environment": {Slug: "shop", Key: "STRIPE_API_KEY", Environment: "staging"},
		"a neighbouring folder":      {Slug: "shop", Folder: "/web", Key: "STRIPE_API_KEY"},
		"another variable":           {Slug: "shop", Key: "STRIPE_WEBHOOK_SECRET"},
		"another project":            {Slug: "other", Key: "STRIPE_API_KEY"},
	} {
		t.Run(name, func(t *testing.T) {
			store, ddb, _ := newTestStore(t)
			if _, err := store.Set(context.Background(), origin, "sk_live_secret", nil); err != nil {
				t.Fatalf("Set err = %v", err)
			}

			stored, ok := ddb.get(PartitionKey(origin.Slug, store.Class), currentSortKey(origin.canonical()))
			if !ok {
				t.Fatal("the written value is missing")
			}
			moved := maps.Clone(stored)
			moved["pk"] = &ddbtypes.AttributeValueMemberS{Value: PartitionKey(elsewhere.Slug, store.Class)}
			moved["sk"] = &ddbtypes.AttributeValueMemberS{Value: currentSortKey(elsewhere.canonical())}
			ddb.put(moved)

			got, err := store.Get(context.Background(), elsewhere, true)
			if err == nil {
				t.Fatalf("Get at %+v returned %q, want the relocated ciphertext to fail to open", elsewhere, got.Plaintext)
			}
			if got.Plaintext != "" {
				t.Errorf("Get returned plaintext %q alongside its error", got.Plaintext)
			}
		})
	}
}

func TestSetRejectsAnOversizeValue(t *testing.T) {
	store, ddb, _ := newTestStore(t)

	_, err := store.Set(context.Background(), testCoordinate(), strings.Repeat("x", MaxValueBytes+1), nil)
	if err == nil {
		t.Fatal("Set err = nil, want an oversize value rejected")
	}
	if !strings.Contains(err.Error(), "too large") {
		t.Errorf("Set err = %v, want it to say the value is too large", err)
	}
	if len(ddb.transactions) != 0 {
		t.Error("an oversize value reached the table")
	}
}

func deletesIn(transactions [][]ddbtypes.TransactWriteItem) int {
	n := 0
	for _, tx := range transactions {
		for _, w := range tx {
			if w.Delete != nil {
				n++
			}
		}
	}
	return n
}

func containsCoordinate(got []Metadata, want Coordinate) bool {
	for _, m := range got {
		if m.Coordinate == want {
			return true
		}
	}
	return false
}

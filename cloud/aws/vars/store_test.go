package vars

import (
	"context"
	"errors"
	"strings"
	"testing"

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

	// Both writers read version 1. The second commits first, so the item is at
	// version 2 by the time the first writer's transaction runs — exactly the
	// interleaving the condition exists to catch.
	seen := int64(1)
	if _, err := store.Set(context.Background(), c, "committed by the other writer", &seen); err != nil {
		t.Fatalf("Set err = %v", err)
	}
	_, err := store.Set(context.Background(), c, "lost the race", &seen)
	if !errors.Is(err, ErrStaleVersion) {
		t.Fatalf("racing Set err = %v, want ErrStaleVersion", err)
	}

	item, ok := ddb.get(partitionKey(c.Slug, store.Class), currentSortKey(c.canonical()))
	if !ok {
		t.Fatal("current pointer missing")
	}
	if got := numberAttr(item, "version"); got != 2 {
		t.Errorf("version after the losing write = %d, want 2", got)
	}
}

// A blind write reads no version, so nothing in the process can tell it lost;
// only the condition the transaction carries can. Staging a competing write
// between this writer's read and its commit is the interleaving that proves it.
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

	// The prune is computed, not queried: the store never reads history to
	// decide what to drop.
	for _, q := range ddb.queries {
		t.Fatalf("Set queried the table (%v); the pruned version must be computed", q.KeyConditionExpression)
	}

	versions, err := store.Versions(context.Background(), c, false)
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

	for _, v := range []string{"one", "two", "three"} {
		if _, err := store.Set(context.Background(), c, v, nil); err != nil {
			t.Fatalf("Set %q err = %v", v, err)
		}
	}

	versions, err := store.Versions(context.Background(), c, true)
	if err != nil {
		t.Fatalf("Versions err = %v", err)
	}
	if len(versions) != 3 {
		t.Fatalf("got %d versions, want 3", len(versions))
	}
	wantNumbers := []int64{3, 2, 1}
	wantValues := []string{"three", "two", "one"}
	for i, v := range versions {
		if v.Version != wantNumbers[i] {
			t.Errorf("versions[%d].Version = %d, want %d", i, v.Version, wantNumbers[i])
		}
		if v.Plaintext != wantValues[i] {
			t.Errorf("versions[%d].Plaintext = %q, want %q", i, v.Plaintext, wantValues[i])
		}
	}
}

func TestVersionsWithoutRevealNeverDecrypts(t *testing.T) {
	store, _, crypto := newTestStore(t)
	c := testCoordinate()

	if _, err := store.Set(context.Background(), c, "sk_live_secret", nil); err != nil {
		t.Fatalf("Set err = %v", err)
	}
	crypto.decrypts = 0

	versions, err := store.Versions(context.Background(), c, false)
	if err != nil {
		t.Fatalf("Versions err = %v", err)
	}
	if crypto.decrypts != 0 {
		t.Errorf("decrypt calls = %d, want 0 without reveal", crypto.decrypts)
	}
	for i, v := range versions {
		if v.Plaintext != "" {
			t.Errorf("versions[%d].Plaintext = %q, want it withheld", i, v.Plaintext)
		}
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

func TestDeleteRemovesTheCurrentPointerAndKeepsHistory(t *testing.T) {
	store, _, _ := newTestStore(t)
	c := testCoordinate()

	if _, err := store.Set(context.Background(), c, "one", nil); err != nil {
		t.Fatalf("Set err = %v", err)
	}
	if _, err := store.Set(context.Background(), c, "two", nil); err != nil {
		t.Fatalf("Set err = %v", err)
	}

	deleted, err := store.Delete(context.Background(), c)
	if err != nil {
		t.Fatalf("Delete err = %v", err)
	}
	if !deleted {
		t.Error("Delete reported nothing deleted, want it to report the pointer removed")
	}
	if _, err := store.Get(context.Background(), c, false); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get after Delete err = %v, want ErrNotFound", err)
	}

	versions, err := store.Versions(context.Background(), c, false)
	if err != nil {
		t.Fatalf("Versions err = %v", err)
	}
	if len(versions) != 2 {
		t.Errorf("history after Delete = %d entries, want 2 kept", len(versions))
	}
}

func TestDeleteIsIdempotent(t *testing.T) {
	store, _, _ := newTestStore(t)
	deleted, err := store.Delete(context.Background(), testCoordinate())
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

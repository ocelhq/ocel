package vars

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/kms"
)

// TestRevealReadsEveryNamedCellInOneQuery proves the runtime's batched read
// costs one round trip to the table however many keys it names: the whole
// point of a batch is that a function's cold start pays a single query, not
// one per variable.
func TestRevealReadsEveryNamedCellInOneQuery(t *testing.T) {
	store, ddb, crypto := newTestStore(t)
	ctx := context.Background()

	cells := []Coordinate{
		{Slug: "shop", Key: "STRIPE_API_KEY"},
		{Slug: "shop", Key: "WEBHOOK_SECRET"},
		{Slug: "shop", Folder: "/web", Key: "SESSION_KEY"},
	}
	want := map[string]string{"STRIPE_API_KEY": "sk-live", "WEBHOOK_SECRET": "whsec", "SESSION_KEY": "sess"}
	for _, c := range cells {
		if _, err := store.Set(ctx, c, want[c.Key], nil); err != nil {
			t.Fatalf("Set %s: %v", c.Key, err)
		}
	}

	queriesBefore := len(ddb.queries)
	decryptsBefore := crypto.decrypts

	values, err := store.Reveal(ctx, "shop", cells)
	if err != nil {
		t.Fatalf("Reveal: %v", err)
	}

	got := map[string]string{}
	for _, v := range values {
		got[v.Coordinate.Key] = v.Plaintext
	}
	for key, plaintext := range want {
		if got[key] != plaintext {
			t.Errorf("Reveal gave %s = %q, want %q", key, got[key], plaintext)
		}
	}
	if len(values) != len(cells) {
		t.Errorf("Reveal returned %d values, want %d", len(values), len(cells))
	}
	if queries := len(ddb.queries) - queriesBefore; queries != 1 {
		t.Errorf("Reveal made %d queries, want 1", queries)
	}
	if decrypts := crypto.decrypts - decryptsBefore; decrypts != len(cells) {
		t.Errorf("Reveal made %d decrypts, want %d (KMS has no batch decrypt)", decrypts, len(cells))
	}
}

// TestRevealPresentsEachCellsOwnEncryptionContext proves the batch decrypts
// through the same context every single read binds, per cell rather than once
// for the batch. The fake key enforces the context the way KMS does, so a
// batch that reused one cell's context for another's blob fails here rather
// than in production against a key nobody can debug.
func TestRevealPresentsEachCellsOwnEncryptionContext(t *testing.T) {
	store, _, crypto := newTestStore(t)
	ctx := context.Background()

	root := Coordinate{Slug: "shop", Key: "API_KEY"}
	scoped := Coordinate{Slug: "shop", Folder: "/web", Key: "API_KEY"}
	if _, err := store.Set(ctx, root, "root-value", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Set(ctx, scoped, "web-value", nil); err != nil {
		t.Fatal(err)
	}
	crypto.decryptContexts = nil

	values, err := store.Reveal(ctx, "shop", []Coordinate{root, scoped})
	if err != nil {
		t.Fatalf("Reveal: %v", err)
	}

	byFolder := map[string]string{}
	for _, v := range values {
		byFolder[v.Coordinate.Folder] = v.Plaintext
	}
	if byFolder[""] != "root-value" {
		t.Errorf("root cell = %q, want %q", byFolder[""], "root-value")
	}
	if byFolder["/web"] != "web-value" {
		t.Errorf("scoped cell = %q, want %q", byFolder["/web"], "web-value")
	}

	folders := map[string]bool{}
	for _, c := range crypto.decryptContexts {
		folders[c["folder"]] = true
		if c["environment"] != classWideEnvironment {
			t.Errorf("decrypt presented environment %q, want the class-wide sentinel %q", c["environment"], classWideEnvironment)
		}
		if c["slug"] != "shop" {
			t.Errorf("decrypt presented slug %q, want %q", c["slug"], "shop")
		}
	}
	if !folders[rootFolder] || !folders["/web"] {
		t.Errorf("decrypt contexts named folders %v, want both %q and %q", folders, rootFolder, "/web")
	}
}

// TestRevealOmitsCellsThatHoldNoValue proves a live key nobody has set yet is
// absent rather than an error. A missing value is the app's schema's business:
// the store reports what is there, and a required key that is not there fails
// where the schema is, not here.
func TestRevealOmitsCellsThatHoldNoValue(t *testing.T) {
	store, _, crypto := newTestStore(t)
	ctx := context.Background()

	set := Coordinate{Slug: "shop", Key: "SET"}
	deleted := Coordinate{Slug: "shop", Key: "DELETED"}
	never := Coordinate{Slug: "shop", Key: "NEVER"}
	if _, err := store.Set(ctx, set, "here", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Set(ctx, deleted, "gone", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Delete(ctx, deleted, nil); err != nil {
		t.Fatal(err)
	}
	decryptsBefore := crypto.decrypts

	values, err := store.Reveal(ctx, "shop", []Coordinate{set, deleted, never})
	if err != nil {
		t.Fatalf("Reveal: %v", err)
	}
	if len(values) != 1 || values[0].Coordinate.Key != "SET" || values[0].Plaintext != "here" {
		t.Fatalf("Reveal = %+v, want only SET=here", values)
	}
	if decrypts := crypto.decrypts - decryptsBefore; decrypts != 1 {
		t.Errorf("Reveal made %d decrypts, want 1: a cell with no value has nothing to decrypt", decrypts)
	}
}

// TestRevealReadsOnlyTheCellsItWasNamed proves the batch is addressed, not a
// dump of the partition: a project's other values are read past, never handed
// back and never decrypted, so a function receives exactly the keys it
// declared.
func TestRevealReadsOnlyTheCellsItWasNamed(t *testing.T) {
	store, _, crypto := newTestStore(t)
	ctx := context.Background()

	wanted := Coordinate{Slug: "shop", Key: "WANTED"}
	other := Coordinate{Slug: "shop", Key: "SOMEONE_ELSES"}
	if _, err := store.Set(ctx, wanted, "mine", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Set(ctx, other, "theirs", nil); err != nil {
		t.Fatal(err)
	}
	decryptsBefore := crypto.decrypts

	values, err := store.Reveal(ctx, "shop", []Coordinate{wanted})
	if err != nil {
		t.Fatalf("Reveal: %v", err)
	}
	if len(values) != 1 || values[0].Coordinate.Key != "WANTED" {
		t.Fatalf("Reveal = %+v, want only WANTED", values)
	}
	if decrypts := crypto.decrypts - decryptsBefore; decrypts != 1 {
		t.Errorf("Reveal made %d decrypts, want 1: an unnamed cell is never decrypted", decrypts)
	}
}

// TestRevealRejectsTheClassWideSentinel proves the batch validates every
// coordinate the way a point read does. A manifest that spelled the class-wide
// environment literally is the plausible bug, and it must be refused here
// rather than addressing a cell named "*".
func TestRevealRejectsTheClassWideSentinel(t *testing.T) {
	store, ddb, _ := newTestStore(t)

	_, err := store.Reveal(context.Background(), "shop", []Coordinate{
		{Slug: "shop", Key: "OK"},
		{Slug: "shop", Key: "BAD", Environment: classWideEnvironment},
	})
	if err == nil {
		t.Fatal("Reveal accepted the class-wide sentinel as an environment name")
	}
	if len(ddb.queries) != 0 {
		t.Errorf("Reveal queried the table %d times before rejecting a bad coordinate, want 0", len(ddb.queries))
	}
}

// TestRevealNamesNoCellsMakesNoCall proves a function that declares no live
// values makes no call at all: it is what keeps a store outage confined to the
// functions that actually depend on the store.
func TestRevealNamesNoCellsMakesNoCall(t *testing.T) {
	store, ddb, crypto := newTestStore(t)

	values, err := store.Reveal(context.Background(), "shop", nil)
	if err != nil {
		t.Fatalf("Reveal: %v", err)
	}
	if len(values) != 0 {
		t.Errorf("Reveal = %+v, want nothing", values)
	}
	if len(ddb.queries) != 0 || crypto.decrypts != 0 {
		t.Errorf("Reveal made %d queries and %d decrypts, want none", len(ddb.queries), crypto.decrypts)
	}
}

// failingDecrypt is a key that opens nothing, standing in for a decrypt the
// function's role is not granted.
type failingDecrypt struct{ CryptoAPI }

func (failingDecrypt) Decrypt(context.Context, *kms.DecryptInput, ...func(*kms.Options)) (*kms.DecryptOutput, error) {
	return nil, errors.New("AccessDeniedException")
}

// TestRevealFailsWhenAnyCellFailsToDecrypt proves the batch is all-or-nothing.
// Handing back the values that happened to open would leave the application
// with a variable silently unset, which at the point of use is indistinguishable
// from one that was never required.
func TestRevealFailsWhenAnyCellFailsToDecrypt(t *testing.T) {
	store, _, _ := newTestStore(t)
	ctx := context.Background()
	cell := Coordinate{Slug: "shop", Key: "API_KEY"}
	if _, err := store.Set(ctx, cell, "sk-live", nil); err != nil {
		t.Fatal(err)
	}

	store.KMS = failingDecrypt{}
	if _, err := store.Reveal(ctx, "shop", []Coordinate{cell}); err == nil {
		t.Fatal("Reveal absorbed a decrypt failure")
	}
}

// erroringQuery is a table that cannot be reached.
type erroringQuery struct{ DynamoAPI }

func (erroringQuery) Query(context.Context, *dynamodb.QueryInput, ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	return nil, errors.New("dial tcp: connection refused")
}

func TestRevealReportsAnUnreachableTable(t *testing.T) {
	store, _, _ := newTestStore(t)
	store.Dynamo = erroringQuery{}

	if _, err := store.Reveal(context.Background(), "shop", []Coordinate{{Slug: "shop", Key: "K"}}); err == nil {
		t.Fatal("Reveal absorbed an unreachable table")
	}
}

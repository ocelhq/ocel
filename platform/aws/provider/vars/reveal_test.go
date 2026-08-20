package vars

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/kms"
)

func TestReveal(t *testing.T) {
	t.Run("reads every named cell in one query", func(t *testing.T) {
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
	})

	t.Run("presents each cells own encryption context", func(t *testing.T) {
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
			if c["environment"] != ClassWideEnvironment {
				t.Errorf("decrypt presented environment %q, want the class-wide sentinel %q", c["environment"], ClassWideEnvironment)
			}
			if c["slug"] != "shop" {
				t.Errorf("decrypt presented slug %q, want %q", c["slug"], "shop")
			}
		}
		if !folders[rootFolder] || !folders["/web"] {
			t.Errorf("decrypt contexts named folders %v, want both %q and %q", folders, rootFolder, "/web")
		}
	})

	t.Run("omits cells that hold no value", func(t *testing.T) {
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
	})

	t.Run("reads only the cells it was named", func(t *testing.T) {
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
	})

	t.Run("resolves a reference inside the project from the query it already made", func(t *testing.T) {
		store, ddb, crypto := newTestStore(t)
		ctx := context.Background()

		origin := Coordinate{Slug: "shop", Key: "STRIPE_API_KEY"}
		alias := Coordinate{Slug: "shop", Folder: "/checkout", Key: "STRIPE_API_KEY"}
		if _, err := store.Set(ctx, origin, "sk-live", nil); err != nil {
			t.Fatal(err)
		}
		if _, err := store.SetReference(ctx, alias, origin, nil); err != nil {
			t.Fatal(err)
		}

		queriesBefore, decryptsBefore := len(ddb.queries), crypto.decrypts

		values, err := store.Reveal(ctx, "shop", []Coordinate{{Folder: alias.Folder, Key: alias.Key}})
		if err != nil {
			t.Fatalf("Reveal: %v", err)
		}
		if len(values) != 1 || values[0].Plaintext != "sk-live" {
			t.Fatalf("Reveal = %+v, want the origin's value", values)
		}
		if queries := len(ddb.queries) - queriesBefore; queries != 1 {
			t.Errorf("Reveal made %d queries, want 1: the target sits in the partition already read", queries)
		}
		if decrypts := crypto.decrypts - decryptsBefore; decrypts != 1 {
			t.Errorf("Reveal made %d decrypts, want 1", decrypts)
		}
	})

	t.Run("reads a shared target once however many cells point at it", func(t *testing.T) {
		store, ddb, crypto := newTestStore(t)
		ctx := context.Background()

		origin := Coordinate{Slug: "platform", Key: "STRIPE_API_KEY"}
		aliases := []Coordinate{
			{Slug: "shop", Folder: "/checkout", Key: "STRIPE_API_KEY"},
			{Slug: "shop", Folder: "/web", Key: "STRIPE_API_KEY"},
			{Slug: "shop", Folder: "/admin", Key: "STRIPE_API_KEY"},
		}
		if _, err := store.Set(ctx, origin, "sk-live", nil); err != nil {
			t.Fatal(err)
		}
		for _, alias := range aliases {
			if _, err := store.SetReference(ctx, alias, origin, nil); err != nil {
				t.Fatalf("SetReference %s: %v", alias, err)
			}
		}

		queriesBefore, decryptsBefore := len(ddb.queries), crypto.decrypts

		values, err := store.Reveal(ctx, "shop", aliases)
		if err != nil {
			t.Fatalf("Reveal: %v", err)
		}
		if len(values) != len(aliases) {
			t.Fatalf("Reveal returned %d values, want %d", len(values), len(aliases))
		}
		for _, v := range values {
			if v.Plaintext != "sk-live" {
				t.Errorf("%s revealed %q, want the origin's value", v.Coordinate, v.Plaintext)
			}
		}
		if queries := len(ddb.queries) - queriesBefore; queries != 2 {
			t.Errorf("Reveal made %d queries, want 2: the partition plus one read of the shared target", queries)
		}
		if decrypts := crypto.decrypts - decryptsBefore; decrypts != 1 {
			t.Errorf("Reveal made %d decrypts, want 1: one ciphertext under one encryption context", decrypts)
		}
	})

	t.Run("naming no cells makes no call", func(t *testing.T) {
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
	})

	t.Run("fails when any cell fails to decrypt", func(t *testing.T) {
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
	})

	t.Run("reports an unreachable table", func(t *testing.T) {
		store, _, _ := newTestStore(t)
		store.Dynamo = erroringQuery{}

		if _, err := store.Reveal(context.Background(), "shop", []Coordinate{{Slug: "shop", Key: "K"}}); err == nil {
			t.Fatal("Reveal absorbed an unreachable table")
		}
	})
}

type failingDecrypt struct{ CryptoAPI }

func (failingDecrypt) Decrypt(context.Context, *kms.DecryptInput, ...func(*kms.Options)) (*kms.DecryptOutput, error) {
	return nil, errors.New("AccessDeniedException")
}

type erroringQuery struct{ DynamoAPI }

func (erroringQuery) Query(context.Context, *dynamodb.QueryInput, ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	return nil, errors.New("dial tcp: connection refused")
}

package vars

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/ocelhq/ocel/pkg/naming"
)

func linkRecords() []Record {
	return []Record{
		{
			Name:       "main",
			Type:       naming.TokenPostgres,
			Properties: map[string]string{"connectionString": "postgres://u:p@h:5432/d"},
			Grants: []Grant{{
				Actions:   []string{"rds-db:connect"},
				Resources: []string{"arn:aws:rds-db:us-east-1:1234:dbuser:db-ABCD/ocel"},
				Label:     "connect",
			}},
		},
		{
			Name:       "uploads",
			Type:       naming.TokenBucket,
			Properties: map[string]string{"bucket": "shop-uploads"},
			Grants: []Grant{{
				Actions:   []string{"s3:GetObject", "s3:PutObject"},
				Resources: []string{"arn:aws:s3:::shop-uploads/*"},
				Label:     "read/write",
			}},
		},
	}
}

func recordNames(records []Record) []string {
	names := make([]string, 0, len(records))
	for _, r := range records {
		names = append(names, r.Name)
	}
	return names
}

func publish(t *testing.T, s *Store, environment string, records []Record) {
	t.Helper()
	if _, err := s.PublishRecords(context.Background(), "shop", environment, OwnerOcel, records); err != nil {
		t.Fatalf("PublishRecords %q: %v", environment, err)
	}
}

func resolve(t *testing.T, s *Store, environment string, names ...string) []PublishedRecord {
	t.Helper()
	got, err := s.ResolveRecords(context.Background(), "shop", environment, names)
	if err != nil {
		t.Fatalf("ResolveRecords %q: %v", environment, err)
	}
	return got
}

func TestPublishRecords(t *testing.T) {
	t.Run("gives every link its own partition", func(t *testing.T) {
		store, ddb, _ := newTestStore(t)

		publish(t, store, "", linkRecords())

		for _, r := range linkRecords() {
			pk := naming.LinkVarsKey("shop", store.Class, r.Name)
			if len(ddb.items[pk]) != 2 {
				t.Errorf("%s holds %d rows, want the value row and the record row", pk, len(ddb.items[pk]))
			}
		}
		if shared := PartitionKey("shop", store.Class); len(ddb.items[shared]) > 1 {
			t.Errorf("%d rows landed in the user partition %s; only the link index belongs there", len(ddb.items[shared]), shared)
		}
	})

	t.Run("writes both rows of a link in one transaction", func(t *testing.T) {
		store, ddb, _ := newTestStore(t)

		publish(t, store, "", linkRecords()[:1])

		pk := naming.LinkVarsKey("shop", store.Class, "main")
		var pairs int
		for _, tx := range ddb.transactions {
			var wrote []string
			for _, w := range tx {
				if w.Update != nil && stringAttr(w.Update.Key, "pk") == pk {
					wrote = append(wrote, stringAttr(w.Update.Key, "sk"))
				}
			}
			if len(wrote) == 0 {
				continue
			}
			pairs++
			slices.Sort(wrote)
			if len(wrote) != 2 || !strings.HasPrefix(wrote[1], currentPrefix) || !strings.HasPrefix(wrote[0], recordPrefix) {
				t.Errorf("a transaction wrote %v into %s; the pair is one value row and one record row", wrote, pk)
			}
		}
		if pairs != 1 {
			t.Errorf("the pair landed over %d transactions; a torn record is observable in every gap", pairs)
		}
	})

	t.Run("leaves no credential at rest", func(t *testing.T) {
		store, ddb, _ := newTestStore(t)

		publish(t, store, "", linkRecords())

		for pk, sks := range ddb.items {
			for sk, item := range sks {
				for name, attr := range item {
					s, ok := attr.(*ddbtypes.AttributeValueMemberS)
					if ok && strings.Contains(s.Value, "postgres://u:p@h:5432/d") {
						t.Fatalf("connection string at rest in %s/%s attribute %q", pk, sk, name)
					}
				}
			}
		}
	})

	t.Run("resolves what it published", func(t *testing.T) {
		store, _, _ := newTestStore(t)
		want := linkRecords()

		publish(t, store, "", want)

		got := resolve(t, store, "", recordNames(want)...)
		if len(got) != len(want) {
			t.Fatalf("ResolveRecords returned %d records, want %d", len(got), len(want))
		}
		for i, p := range got {
			if p.Type != want[i].Type {
				t.Errorf("%s = type %q, want %q", p.Name, p.Type, want[i].Type)
			}
			for key, value := range want[i].Properties {
				if p.Properties[key] != value {
					t.Errorf("%s property %s = %q, want %q", p.Name, key, p.Properties[key], value)
				}
			}
			if !slices.EqualFunc(p.Grants, want[i].Grants, sameGrant) {
				t.Errorf("%s grants = %+v, want %+v", p.Name, p.Grants, want[i].Grants)
			}
		}
	})

	t.Run("counts the version of the pair up on every publish", func(t *testing.T) {
		store, ddb, _ := newTestStore(t)

		publish(t, store, "", linkRecords()[:1])
		publish(t, store, "", linkRecords()[:1])
		publish(t, store, "", linkRecords()[:1])

		got := resolve(t, store, "", "main")
		if got[0].Version != 3 {
			t.Errorf("version = %d after three publishes, want 3", got[0].Version)
		}
		pk := naming.LinkVarsKey("shop", store.Class, "main")
		for sk, item := range ddb.items[pk] {
			if v := numberAttr(item, "version"); v != 3 {
				t.Errorf("%s sits at version %d while its pair reports 3", sk, v)
			}
		}
	})

	t.Run("counts a publish it never read", func(t *testing.T) {
		store, ddb, _ := newTestStore(t)
		publish(t, store, "", linkRecords()[:1])

		ddb.beforeTransact = func() {
			publish(t, store, "", linkRecords()[:1])
		}
		publish(t, store, "", linkRecords()[:1])

		got := resolve(t, store, "", "main")
		if got[0].Version != 3 {
			t.Errorf("version = %d, want 3 — a publish that read the version first loses the one racing it", got[0].Version)
		}
	})

	t.Run("never exposes half of a publish", func(t *testing.T) {
		store, ddb, _ := newTestStore(t)
		first := linkRecords()[:1]
		publish(t, store, "", first)

		second := linkRecords()[:1]
		second[0].Properties = map[string]string{"connectionString": "postgres://u:p@rotated:5432/d"}
		second[0].Grants = []Grant{{Actions: []string{"rds-db:connect"}, Resources: []string{"arn:aws:rds-db:us-east-1:1234:dbuser:db-EFGH/ocel"}}}

		var midflight []PublishedRecord
		ddb.beforeTransact = func() { midflight = resolve(t, store, "", "main") }
		publish(t, store, "", second)

		if len(midflight) != 1 {
			t.Fatalf("a read during the publish saw %d records, want the link it had all along", len(midflight))
		}
		if got := midflight[0].Properties["connectionString"]; got != first[0].Properties["connectionString"] {
			t.Errorf("value = %q mid-publish, want the published pair or the previous one, never a mix", got)
		}
		if !slices.EqualFunc(midflight[0].Grants, first[0].Grants, sameGrant) {
			t.Errorf("grants = %+v mid-publish, want the ones the value was published with", midflight[0].Grants)
		}
	})

	t.Run("prunes the links a deploy no longer publishes", func(t *testing.T) {
		store, ddb, _ := newTestStore(t)

		publish(t, store, "", linkRecords())
		pruned, err := store.PublishRecords(context.Background(), "shop", "", OwnerOcel, linkRecords()[:1])
		if err != nil {
			t.Fatalf("PublishRecords: %v", err)
		}
		if pruned != 2 {
			t.Errorf("pruned = %d rows, want the dropped link's whole pair", pruned)
		}
		if pk := naming.LinkVarsKey("shop", store.Class, "uploads"); len(ddb.items[pk]) != 0 {
			t.Errorf("%s still holds %d rows after the link left the manifest", pk, len(ddb.items[pk]))
		}
		if pk := naming.LinkVarsKey("shop", store.Class, "main"); len(ddb.items[pk]) == 0 {
			t.Errorf("pruning took %s with it", pk)
		}
	})

	t.Run("prunes only the environment it publishes for", func(t *testing.T) {
		store, _, _ := newTestStore(t)
		mine := []Record{{Name: "other", Type: naming.TokenBucket, Properties: map[string]string{"bucket": "b"}}}
		theirs := []Record{{Name: "main", Type: naming.TokenBucket, Properties: map[string]string{"bucket": "c"}}}

		publish(t, store, "pr-42", theirs)
		publish(t, store, "pr-1", theirs)
		publish(t, store, "pr-1", mine)

		got := resolve(t, store, "pr-42", "main")
		if len(got) != 1 || got[0].Properties["bucket"] != "c" {
			t.Errorf("pr-42 read %+v, want its own pair — a concurrent pr-1 deploy pruned it", got)
		}
	})
}

func TestResolveRecordsShadowing(t *testing.T) {
	classWide := []Record{{
		Name:       "main",
		Type:       naming.TokenPostgres,
		Properties: map[string]string{"connectionString": "postgres://shared", "sslmode": "require"},
		Grants:     []Grant{{Actions: []string{"rds-db:connect"}, Resources: []string{"arn:shared"}}},
	}}
	named := []Record{{
		Name:       "main",
		Type:       naming.TokenPostgres,
		Properties: map[string]string{"connectionString": "postgres://staging"},
		Grants:     []Grant{{Actions: []string{"rds-db:connect"}, Resources: []string{"arn:staging"}}},
	}}

	t.Run("serves every environment of the class from one publish", func(t *testing.T) {
		store, _, _ := newTestStore(t)
		publish(t, store, "", classWide)

		for _, environment := range []string{"", "staging", "pr-42"} {
			got := resolve(t, store, environment, "main")
			if len(got) != 1 || got[0].Properties["connectionString"] != "postgres://shared" {
				t.Errorf("%q read %+v, want the class-wide pair — an ephemeral preview publishes nothing of its own", environment, got)
			}
			if got[0].Environment != "" {
				t.Errorf("%q resolved from environment %q, want the class-wide coordinate", environment, got[0].Environment)
			}
		}
	})

	t.Run("a named pair shadows the class-wide pair whole", func(t *testing.T) {
		store, _, _ := newTestStore(t)
		publish(t, store, "", classWide)
		publish(t, store, "staging", named)

		got := resolve(t, store, "staging", "main")
		if len(got) != 1 {
			t.Fatalf("ResolveRecords returned %d records, want 1", len(got))
		}
		if got[0].Properties["connectionString"] != "postgres://staging" {
			t.Errorf("connectionString = %q, want staging's own", got[0].Properties["connectionString"])
		}
		if _, merged := got[0].Properties["sslmode"]; merged {
			t.Errorf("properties = %+v, want only staging's publish — a link is one coherent fact, not a merge of two", got[0].Properties)
		}
		if !slices.EqualFunc(got[0].Grants, named[0].Grants, sameGrant) {
			t.Errorf("grants = %+v, want the ones staging published beside its values", got[0].Grants)
		}
		if got[0].Environment != "staging" {
			t.Errorf("resolved environment = %q, want staging", got[0].Environment)
		}

		if wide := resolve(t, store, "", "main"); wide[0].Properties["connectionString"] != "postgres://shared" {
			t.Errorf("the class-wide pair read %+v after staging shadowed it", wide[0])
		}
	})

	t.Run("shadowing takes hold in one step", func(t *testing.T) {
		store, ddb, _ := newTestStore(t)
		publish(t, store, "", classWide)

		var midflight []PublishedRecord
		ddb.beforeTransact = func() { midflight = resolve(t, store, "staging", "main") }
		publish(t, store, "staging", named)

		if len(midflight) != 1 {
			t.Fatalf("a read during the shadowing publish saw %d records, want 1", len(midflight))
		}
		if got := midflight[0].Properties["connectionString"]; got != "postgres://shared" {
			t.Errorf("value = %q mid-publish, want the class-wide pair until the named one lands whole", got)
		}
		if !slices.EqualFunc(midflight[0].Grants, classWide[0].Grants, sameGrant) {
			t.Errorf("grants = %+v mid-publish, want the class-wide pair's own", midflight[0].Grants)
		}
	})
}

func TestResolveRecordsVerifies(t *testing.T) {
	t.Run("names a link nothing published", func(t *testing.T) {
		store, _, _ := newTestStore(t)
		publish(t, store, "", linkRecords()[:1])

		_, err := store.ResolveRecords(context.Background(), "shop", "", []string{"main", "uploads"})
		if !errors.Is(err, ErrNotPublished) {
			t.Fatalf("err = %v, want %v", err, ErrNotPublished)
		}
		if !strings.Contains(err.Error(), "uploads") {
			t.Errorf("err = %v, want the link that is missing named", err)
		}
	})

	t.Run("rejects an unscoped grant a publish never went through", func(t *testing.T) {
		for name, grant := range unscopedGrants() {
			t.Run(name, func(t *testing.T) {
				store, ddb, _ := newTestStore(t)
				publish(t, store, "", linkRecords()[1:])
				overwriteRecordRow(t, ddb, store, "uploads", "", recordRow{Type: naming.TokenBucket, Grants: []Grant{grant}})

				_, err := store.ResolveRecords(context.Background(), "shop", "", []string{"uploads"})
				if !errors.Is(err, ErrUnscopedGrant) {
					t.Fatalf("err = %v, want %v", err, ErrUnscopedGrant)
				}
				if !strings.Contains(err.Error(), "uploads") {
					t.Errorf("err = %v, want the link whose grant is unscoped named", err)
				}
			})
		}
	})

	t.Run("rejects a value no key will open", func(t *testing.T) {
		store, ddb, _ := newTestStore(t)
		publish(t, store, "", linkRecords()[:1])

		c := linkCoordinate("shop", "main", "").canonical()
		row, _ := ddb.get(c.partition(store.Class), currentSortKey(c))
		row["ciphertext"] = &ddbtypes.AttributeValueMemberB{Value: []byte("tampered")}
		ddb.put(row)

		_, err := store.ResolveRecords(context.Background(), "shop", "", []string{"main"})
		if !errors.Is(err, ErrUnreadableRecord) {
			t.Fatalf("err = %v, want %v", err, ErrUnreadableRecord)
		}
	})

	t.Run("keeps a grant scoped by prefix", func(t *testing.T) {
		store, _, _ := newTestStore(t)
		publish(t, store, "", linkRecords()[1:])

		got := resolve(t, store, "", "uploads")
		if len(got) != 1 {
			t.Fatalf("ResolveRecords returned %d records, want the bucket's own arn prefix accepted", len(got))
		}
	})

	t.Run("rejects a bag nothing can parse", func(t *testing.T) {
		store, ddb, _ := newTestStore(t)
		publish(t, store, "", linkRecords()[:1])
		corruptValue(t, ddb, store, "main", "", "not a property bag")

		_, err := store.ResolveRecords(context.Background(), "shop", "", []string{"main"})
		if !errors.Is(err, ErrUnreadableRecord) {
			t.Fatalf("err = %v, want %v", err, ErrUnreadableRecord)
		}
		if !strings.Contains(err.Error(), "main") {
			t.Errorf("err = %v, want the link whose bag will not parse named", err)
		}
	})

	t.Run("rejects a record row nothing can parse", func(t *testing.T) {
		store, ddb, _ := newTestStore(t)
		publish(t, store, "", linkRecords()[:1])

		pk := naming.LinkVarsKey("shop", store.Class, "main")
		row, _ := ddb.get(pk, recordSortKey(""))
		row["record"] = &ddbtypes.AttributeValueMemberS{Value: "{"}
		ddb.put(row)

		_, err := store.ResolveRecords(context.Background(), "shop", "", []string{"main"})
		if !errors.Is(err, ErrUnreadableRecord) {
			t.Fatalf("err = %v, want %v", err, ErrUnreadableRecord)
		}
	})

	t.Run("rejects a record whose value row went missing", func(t *testing.T) {
		store, ddb, _ := newTestStore(t)
		publish(t, store, "", linkRecords()[:1])

		pk := naming.LinkVarsKey("shop", store.Class, "main")
		delete(ddb.items[pk], currentSortKey(linkCoordinate("shop", "main", "").canonical()))

		_, err := store.ResolveRecords(context.Background(), "shop", "", []string{"main"})
		if !errors.Is(err, ErrUnreadableRecord) {
			t.Fatalf("err = %v, want %v", err, ErrUnreadableRecord)
		}
	})
}

func TestPublishRecordsRefuses(t *testing.T) {
	store, _, _ := newTestStore(t)

	for name, tc := range map[string]struct {
		environment string
		record      Record
	}{
		"no project link":   {record: Record{Type: naming.TokenBucket}},
		"no type token":     {record: Record{Name: "main"}},
		"reserved env":      {environment: classWideEnvironment, record: linkRecords()[0]},
		"delimited name":    {record: Record{Name: "a" + delimiter + "b", Type: naming.TokenBucket}},
		"delimited env":     {environment: "a" + delimiter + "b", record: linkRecords()[0]},
		"property overflow": {record: Record{Name: "main", Type: naming.TokenBucket, Properties: map[string]string{"blob": strings.Repeat("x", MaxValueBytes)}}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := store.PublishRecords(context.Background(), "shop", tc.environment, OwnerOcel, []Record{tc.record}); err == nil {
				t.Fatalf("PublishRecords accepted %+v", tc.record)
			}
		})
	}

	t.Run("no slug", func(t *testing.T) {
		if _, err := store.PublishRecords(context.Background(), "", "", OwnerOcel, linkRecords()); err == nil {
			t.Fatal("PublishRecords wrote a pair addressed to no project")
		}
	})

	t.Run("the same link twice", func(t *testing.T) {
		store, ddb, _ := newTestStore(t)
		twice := []Record{linkRecords()[0], linkRecords()[0]}
		twice[1].Properties = map[string]string{"connectionString": "postgres://other"}

		if _, err := store.PublishRecords(context.Background(), "shop", "", OwnerOcel, twice); err == nil {
			t.Fatal("PublishRecords raced two publishes of one link onto the same rows")
		}
		if pk := naming.LinkVarsKey("shop", store.Class, "main"); len(ddb.items[pk]) != 0 {
			t.Errorf("%s holds %d rows; a batch it refused should have written nothing", pk, len(ddb.items[pk]))
		}
	})

	t.Run("an unscoped grant", func(t *testing.T) {
		for name, grant := range unscopedGrants() {
			t.Run(name, func(t *testing.T) {
				store, ddb, _ := newTestStore(t)
				record := linkRecords()[1]
				record.Grants = []Grant{grant}

				_, err := store.PublishRecords(context.Background(), "shop", "", OwnerOcel, []Record{record})
				if !errors.Is(err, ErrUnscopedGrant) {
					t.Fatalf("err = %v, want %v — the deploy that introduces the grant is the one that can still fix it", err, ErrUnscopedGrant)
				}
				if pk := naming.LinkVarsKey("shop", store.Class, "uploads"); len(ddb.items[pk]) != 0 {
					t.Errorf("%s holds %d rows; an unscoped grant reached the store and every cold start after it", pk, len(ddb.items[pk]))
				}
			})
		}
	})

}

func unscopedGrants() map[string]Grant {
	return map[string]Grant{
		"every resource": {Actions: []string{"s3:GetObject"}, Resources: []string{"*"}},
		"no resource":    {Actions: []string{"s3:GetObject"}},
		"every action":   {Actions: []string{"*"}, Resources: []string{"arn:aws:s3:::shop-uploads/*"}},
		"no action":      {Resources: []string{"arn:aws:s3:::shop-uploads/*"}},
		"every service":  {Actions: []string{"*:*"}, Resources: []string{"arn:aws:s3:::shop-uploads/*"}},
		"whole service":  {Actions: []string{"s3:*"}, Resources: []string{"arn:aws:s3:::shop-uploads/*"}},
	}
}

func TestPublishRecordsSurvivesACancelledTransaction(t *testing.T) {
	t.Run("publishes the pair once DynamoDB stops cancelling it", func(t *testing.T) {
		store, ddb, _ := newTestStore(t)
		ddb.cancelTransacts = linkAttempts - 1

		publish(t, store, "", linkRecords()[:1])

		if got := resolve(t, store, "", "main"); got[0].Version != 1 {
			t.Errorf("version = %d, want the one publish that landed", got[0].Version)
		}
	})

	t.Run("names the race rather than the SDK error", func(t *testing.T) {
		store, ddb, _ := newTestStore(t)
		ddb.cancelTransacts = linkAttempts

		_, err := store.PublishRecords(context.Background(), "shop", "", OwnerOcel, linkRecords()[:1])
		if err == nil {
			t.Fatal("PublishRecords reported success while every transaction was cancelled")
		}
		if !strings.Contains(err.Error(), "racing") {
			t.Errorf("err = %v, want a refusal naming the racing deploy", err)
		}
	})
}

func TestResolveRecordsNeverMergesAPair(t *testing.T) {
	t.Run("refuses a record and a value from different publishes", func(t *testing.T) {
		store, ddb, _ := newTestStore(t)
		publish(t, store, "", linkRecords()[:1])

		pk := naming.LinkVarsKey("shop", store.Class, "main")
		row, _ := ddb.get(pk, recordSortKey(""))
		row["version"] = &ddbtypes.AttributeValueMemberN{Value: "2"}
		ddb.put(row)

		got, err := store.ResolveRecords(context.Background(), "shop", "", []string{"main"})
		if got != nil {
			t.Errorf("ResolveRecords returned %+v, want nothing cell-merged out of two publishes", got)
		}
		if !errors.Is(err, ErrUnreadableRecord) {
			t.Fatalf("err = %v, want %v", err, ErrUnreadableRecord)
		}
	})

	t.Run("reads the pair back consistently", func(t *testing.T) {
		store, ddb, _ := newTestStore(t)
		publish(t, store, "", linkRecords()[:1])
		resolve(t, store, "", "main")

		pk := naming.LinkVarsKey("shop", store.Class, "main")
		for _, in := range ddb.queries {
			if stringAttr(in.ExpressionAttributeValues, ":pk") != pk {
				continue
			}
			if in.ConsistentRead == nil || !*in.ConsistentRead {
				t.Fatal("the pair was read eventually-consistently; the two rows can come from either side of a publish")
			}
		}
	})

	t.Run("falls through to the class-wide pair once a teardown finishes", func(t *testing.T) {
		store, ddb, _ := newTestStore(t)
		classWide := []Record{{Name: "main", Type: naming.TokenBucket, Properties: map[string]string{"bucket": "shared"}}}
		preview := []Record{{Name: "main", Type: naming.TokenBucket, Properties: map[string]string{"bucket": "pr-42"}}}
		publish(t, store, "", classWide)
		publish(t, store, "pr-42", preview)

		pk := naming.LinkVarsKey("shop", store.Class, "main")
		c := linkCoordinate("shop", "main", "pr-42").canonical()
		delete(ddb.items[pk], currentSortKey(c))
		ddb.afterQuery = func() { delete(ddb.items[pk], recordSortKey("pr-42")) }

		got := resolve(t, store, "pr-42", "main")
		if len(got) != 1 || got[0].Properties["bucket"] != "shared" {
			t.Errorf("pr-42 read %+v, want the class-wide pair sitting in the same partition", got)
		}
	})
}

func TestPublishRecordsSurvivesAConcurrentDeploy(t *testing.T) {
	seed := func(t *testing.T) (*Store, *fakeDynamo) {
		t.Helper()
		store, ddb, _ := newTestStore(t)
		publish(t, store, "", linkRecords())
		return store, ddb
	}

	raceTheIndexWrite := func(ddb *fakeDynamo, race func(), everyAttempt bool) {
		indexSK := linkIndexSortKey(OwnerOcel, "")
		var arm func()
		arm = func() {
			ddb.armBeforePutTo(indexSK, func() {
				race()
				if everyAttempt {
					arm()
				}
			})
		}
		arm()
	}

	t.Run("re-reads the index a racing deploy moved under it", func(t *testing.T) {
		store, ddb := seed(t)

		raceTheIndexWrite(ddb, func() {
			if _, err := store.PublishRecords(context.Background(), "shop", "", OwnerOcel, linkRecords()); err != nil {
				t.Errorf("racing PublishRecords: %v", err)
			}
		}, false)

		if _, err := store.PublishRecords(context.Background(), "shop", "", OwnerOcel, linkRecords()); err != nil {
			t.Fatalf("PublishRecords lost to a concurrent deploy of the same environment: %v", err)
		}

		index, ok := ddb.get(PartitionKey("shop", store.Class), linkIndexSortKey(OwnerOcel, ""))
		if !ok {
			t.Fatal("no link index survived the race")
		}
		if got := numberAttr(index, "version"); got < 3 {
			t.Errorf("index version = %d, want the seed, the racer and this deploy each committed once — an unconditional put would sit at 1", got)
		}
		for _, r := range linkRecords() {
			pk := naming.LinkVarsKey("shop", store.Class, r.Name)
			if len(ddb.items[pk]) == 0 {
				t.Errorf("%s was pruned away by a deploy that also publishes it", pk)
			}
		}
	})

	t.Run("gives up naming the race rather than pruning on a stale read", func(t *testing.T) {
		store, ddb := seed(t)

		raceTheIndexWrite(ddb, func() {
			if _, err := store.PublishRecords(context.Background(), "shop", "", OwnerOcel, linkRecords()); err != nil {
				t.Errorf("racing PublishRecords: %v", err)
			}
		}, true)

		_, err := store.PublishRecords(context.Background(), "shop", "", OwnerOcel, linkRecords())
		if err == nil {
			t.Fatal("PublishRecords reported success while every attempt was overtaken")
		}
		if !strings.Contains(err.Error(), "racing") {
			t.Errorf("err = %v, want a refusal naming the racing deploy", err)
		}
		for _, r := range linkRecords() {
			pk := naming.LinkVarsKey("shop", store.Class, r.Name)
			if len(ddb.items[pk]) == 0 {
				t.Errorf("%s was pruned away by a deploy that never committed its index", pk)
			}
		}
	})
}

func TestLinkCoordinatesAreNotUserEditable(t *testing.T) {
	store, _, _ := newTestStore(t)
	c := Coordinate{Slug: "shop", Link: "main", Key: linkValueKey}

	t.Run("Set", func(t *testing.T) {
		if _, err := store.Set(context.Background(), c, "mine", nil); err == nil {
			t.Fatal("Set wrote into a link partition; derived values are ocel's to write")
		}
	})
	t.Run("Delete", func(t *testing.T) {
		if _, err := store.Delete(context.Background(), c, nil); err == nil {
			t.Fatal("Delete removed a derived value a user never owned")
		}
	})
	t.Run("Get", func(t *testing.T) {
		if _, err := store.Get(context.Background(), c, true); err == nil {
			t.Fatal("Get revealed a derived value through the user read path")
		}
	})
	t.Run("SetReference", func(t *testing.T) {
		if _, err := store.SetReference(context.Background(), Coordinate{Slug: "shop", Key: "DB"}, c, nil); err == nil {
			t.Fatal("SetReference pointed a user value at a derived one, reading it out through a name the user controls")
		}
	})
}

func TestListSkipsDerivedValues(t *testing.T) {
	store, _, _ := newTestStore(t)
	if _, err := store.Set(context.Background(), testCoordinate(), "sk_live", nil); err != nil {
		t.Fatalf("Set: %v", err)
	}
	publish(t, store, "", linkRecords())

	got, err := store.List(context.Background(), "shop")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].Coordinate.Key != "STRIPE_API_KEY" {
		t.Errorf("List = %+v, want only the value the user set", got)
	}
}

func TestPurgeTakesTheLinkPartitions(t *testing.T) {
	store, ddb, _ := newTestStore(t)
	if _, err := store.Set(context.Background(), testCoordinate(), "sk_live", nil); err != nil {
		t.Fatalf("Set: %v", err)
	}
	publish(t, store, "", linkRecords())

	if _, err := store.Purge(context.Background(), "shop"); err != nil {
		t.Fatalf("Purge: %v", err)
	}
	for pk, sks := range ddb.items {
		if len(sks) > 0 {
			t.Errorf("%s still holds %d rows after the project was purged", pk, len(sks))
		}
	}
}

func sameGrant(a, b Grant) bool {
	return slices.Equal(a.Actions, b.Actions) && slices.Equal(a.Resources, b.Resources) && a.Label == b.Label
}

func corruptValue(t *testing.T, ddb *fakeDynamo, store *Store, link, environment, plaintext string) {
	t.Helper()
	c := linkCoordinate("shop", link, environment).canonical()
	sealed, err := store.encrypt(context.Background(), c, plaintext)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	row, ok := ddb.get(c.partition(store.Class), currentSortKey(c))
	if !ok {
		t.Fatalf("no value row at %s", currentSortKey(c))
	}
	row["ciphertext"] = &ddbtypes.AttributeValueMemberB{Value: sealed}
	ddb.put(row)
}

func overwriteRecordRow(t *testing.T, ddb *fakeDynamo, store *Store, link, environment string, row recordRow) {
	t.Helper()
	encoded, err := json.Marshal(row)
	if err != nil {
		t.Fatalf("marshal record row: %v", err)
	}
	stored, ok := ddb.get(LinkPartitionKey("shop", store.Class, link), recordSortKey(environment))
	if !ok {
		t.Fatalf("no record row at %s", recordSortKey(environment))
	}
	stored["record"] = &ddbtypes.AttributeValueMemberS{Value: string(encoded)}
	ddb.put(stored)
}

func TestPublishersPruneOnlyTheirOwnRecords(t *testing.T) {
	t.Run("one publisher's inventory survives another's publish", func(t *testing.T) {
		store, _, _ := newTestStore(t)
		foreign := []Record{{
			Name:       "orders",
			Type:       "sst:aws.Postgres",
			Properties: map[string]string{"connectionString": "postgres://sst/orders"},
		}}
		if _, err := store.PublishRecords(context.Background(), "shop", "", "SST", foreign); err != nil {
			t.Fatalf("PublishRecords SST: %v", err)
		}

		publish(t, store, "", linkRecords())

		got := resolve(t, store, "", "orders")
		if got[0].Type != "sst:aws.Postgres" {
			t.Fatalf("record = %+v, want the foreign publisher's record untouched by ocel's own publish", got[0])
		}
	})

	t.Run("published names read back across every publisher", func(t *testing.T) {
		store, _, _ := newTestStore(t)
		if _, err := store.PublishRecords(context.Background(), "shop", "", "SST", []Record{{
			Name: "orders", Type: "sst:aws.Postgres", Properties: map[string]string{"connectionString": "postgres://sst/orders"},
		}}); err != nil {
			t.Fatalf("PublishRecords SST: %v", err)
		}
		publish(t, store, "pr-42", linkRecords()[:1])

		names, err := store.PublishedNames(context.Background(), "shop", store.Class, "pr-42")
		if err != nil {
			t.Fatalf("PublishedNames: %v", err)
		}
		if !slices.Equal(names, []string{"main", "orders"}) {
			t.Errorf("names = %v, want the preview's own names beside the class-wide ones, whoever published them", names)
		}

		elsewhere, err := store.PublishedNames(context.Background(), "shop", store.Class, "pr-7")
		if err != nil {
			t.Fatalf("PublishedNames: %v", err)
		}
		if !slices.Equal(elsewhere, []string{"orders"}) {
			t.Errorf("names = %v, want a sibling preview to see only what binds class-wide", elsewhere)
		}
	})

	t.Run("an index row from before the per-publisher split binds nothing", func(t *testing.T) {
		store, ddb, _ := newTestStore(t)
		pk := PartitionKey("shop", store.Class)
		ddb.put(marshal(item{
			PK:      pk,
			SK:      linkIndexPrefix + tokenEnv + delimiter + classWideEnvironment,
			Version: 1,
			Names:   []string{"ghost"},
		}))

		publish(t, store, "", linkRecords())

		names, err := store.PublishedNames(context.Background(), "shop", store.Class, "pr-42")
		if err != nil {
			t.Fatalf("PublishedNames: %v", err)
		}
		if slices.Contains(names, "ghost") {
			t.Errorf("names = %v, want a row nothing can prune or rewrite to bind nothing", names)
		}
	})

	t.Run("a name another publisher still claims survives a prune", func(t *testing.T) {
		store, _, _ := newTestStore(t)
		foreign := []Record{{
			Name: "orders", Type: "sst:aws.Postgres", Properties: map[string]string{"connectionString": "postgres://sst/orders"},
		}}
		mine := []Record{{
			Name: "orders", Type: naming.TokenPostgres, Properties: map[string]string{"connectionString": "postgres://ocel/orders"},
		}}
		if _, err := store.PublishRecords(context.Background(), "shop", "", OwnerOcel, mine); err != nil {
			t.Fatalf("PublishRecords ocel: %v", err)
		}
		if _, err := store.PublishRecords(context.Background(), "shop", "", "SST", foreign); err != nil {
			t.Fatalf("PublishRecords SST: %v", err)
		}

		if _, err := store.PublishRecords(context.Background(), "shop", "", OwnerOcel, nil); err != nil {
			t.Fatalf("PublishRecords ocel: %v", err)
		}

		got := resolve(t, store, "", "orders")
		if got[0].Type != "sst:aws.Postgres" {
			t.Fatalf("record = %+v, want the rows a live publisher still claims left alone", got[0])
		}
	})

	t.Run("an unnamed publisher is refused", func(t *testing.T) {
		store, _, _ := newTestStore(t)
		if _, err := store.PublishRecords(context.Background(), "shop", "", "", linkRecords()); err == nil {
			t.Fatal("PublishRecords accepted an unnamed publisher, whose index row every other publisher would prune")
		}
	})
}

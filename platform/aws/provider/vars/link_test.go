package vars

import (
	"context"
	"slices"
	"strings"
	"testing"

	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/ocelhq/ocel/pkg/naming"
)

func linkValues() []LinkValue {
	return []LinkValue{
		{Link: "main", Key: "OCEL_RESOURCE_POSTGRES_main", Value: `{"connectionString":"postgres://u:p@h:5432/d"}`},
		{Link: "uploads", Key: "OCEL_RESOURCE_BUCKET_uploads", Value: `{"bucket":"shop-uploads"}`},
	}
}

func TestPublishLinks(t *testing.T) {
	t.Run("gives every link its own partition", func(t *testing.T) {
		store, ddb, _ := newTestStore(t)

		if _, err := store.PublishLinks(context.Background(), "shop", "", linkValues()); err != nil {
			t.Fatalf("PublishLinks: %v", err)
		}

		for _, l := range linkValues() {
			pk := naming.LinkVarsKey("shop", store.Class, l.Link)
			if len(ddb.items[pk]) == 0 {
				t.Errorf("nothing written to %s; a LeadingKeys condition can only narrow on the partition key", pk)
			}
		}
		if shared := PartitionKey("shop", store.Class); len(ddb.items[shared]) > 1 {
			t.Errorf("%d rows landed in the user partition %s; only the link index belongs there", len(ddb.items[shared]), shared)
		}
	})

	t.Run("leaves no credential at rest", func(t *testing.T) {
		store, ddb, _ := newTestStore(t)

		if _, err := store.PublishLinks(context.Background(), "shop", "", linkValues()); err != nil {
			t.Fatalf("PublishLinks: %v", err)
		}

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

	t.Run("reveals what it published", func(t *testing.T) {
		store, _, _ := newTestStore(t)
		want := linkValues()

		if _, err := store.PublishLinks(context.Background(), "shop", "", want); err != nil {
			t.Fatalf("PublishLinks: %v", err)
		}

		got, err := store.RevealLinks(context.Background(), "shop", linkCells(want, ""))
		if err != nil {
			t.Fatalf("RevealLinks: %v", err)
		}
		if len(got) != len(want) {
			t.Fatalf("RevealLinks returned %d values, want %d", len(got), len(want))
		}
		for _, v := range got {
			i := slices.IndexFunc(want, func(l LinkValue) bool { return l.Key == v.Coordinate.Key })
			if i < 0 {
				t.Fatalf("RevealLinks returned %s, which was never published", v.Coordinate.Key)
			}
			if v.Plaintext != want[i].Value {
				t.Errorf("%s = %q, want %q", v.Coordinate.Key, v.Plaintext, want[i].Value)
			}
		}
	})

	t.Run("an environment's values never shadow another's", func(t *testing.T) {
		store, _, _ := newTestStore(t)
		one := []LinkValue{{Link: "main", Key: "OCEL_RESOURCE_POSTGRES_main", Value: "pr-1"}}
		two := []LinkValue{{Link: "main", Key: "OCEL_RESOURCE_POSTGRES_main", Value: "pr-42"}}

		if _, err := store.PublishLinks(context.Background(), "shop", "pr-1", one); err != nil {
			t.Fatalf("PublishLinks pr-1: %v", err)
		}
		if _, err := store.PublishLinks(context.Background(), "shop", "pr-42", two); err != nil {
			t.Fatalf("PublishLinks pr-42: %v", err)
		}

		got, err := store.RevealLinks(context.Background(), "shop", linkCells(one, "pr-1"))
		if err != nil {
			t.Fatalf("RevealLinks: %v", err)
		}
		if len(got) != 1 || got[0].Plaintext != "pr-1" {
			t.Errorf("pr-1 read %+v, want its own value — the second preview overwrote the first", got)
		}
	})

	t.Run("prunes the links a deploy no longer publishes", func(t *testing.T) {
		store, ddb, _ := newTestStore(t)

		if _, err := store.PublishLinks(context.Background(), "shop", "", linkValues()); err != nil {
			t.Fatalf("PublishLinks: %v", err)
		}
		pruned, err := store.PublishLinks(context.Background(), "shop", "", linkValues()[:1])
		if err != nil {
			t.Fatalf("PublishLinks: %v", err)
		}
		if pruned != 1 {
			t.Errorf("pruned = %d, want 1 — the dropped link's rows stay consumable", pruned)
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
		mine := []LinkValue{{Link: "other", Key: "K", Value: "pr-1"}}
		theirs := []LinkValue{{Link: "main", Key: "K", Value: "pr-42"}}

		if _, err := store.PublishLinks(context.Background(), "shop", "pr-42", theirs); err != nil {
			t.Fatalf("PublishLinks pr-42: %v", err)
		}
		if _, err := store.PublishLinks(context.Background(), "shop", "pr-1", theirs); err != nil {
			t.Fatalf("PublishLinks pr-1: %v", err)
		}
		if _, err := store.PublishLinks(context.Background(), "shop", "pr-1", mine); err != nil {
			t.Fatalf("PublishLinks pr-1: %v", err)
		}

		got, err := store.RevealLinks(context.Background(), "shop", linkCells(theirs, "pr-42"))
		if err != nil {
			t.Fatalf("RevealLinks: %v", err)
		}
		if len(got) != 1 {
			t.Errorf("pr-42 lost its links to a concurrent pr-1 deploy: read %d values, want 1", len(got))
		}
	})
}

func TestPublishLinksSurvivesAConcurrentDeploy(t *testing.T) {
	t.Run("re-reads the index a racing deploy moved under it", func(t *testing.T) {
		store, ddb, _ := newTestStore(t)
		if _, err := store.PublishLinks(context.Background(), "shop", "", linkValues()); err != nil {
			t.Fatalf("seed PublishLinks: %v", err)
		}

		racer := func() {
			if _, err := store.PublishLinks(context.Background(), "shop", "", linkValues()); err != nil {
				t.Errorf("racing PublishLinks: %v", err)
			}
		}
		ddb.beforePut = func() { ddb.beforePut = nil; racer() }

		if _, err := store.PublishLinks(context.Background(), "shop", "", linkValues()); err != nil {
			t.Fatalf("PublishLinks lost to a concurrent deploy of the same environment: %v", err)
		}

		index, ok := ddb.get(PartitionKey("shop", store.Class), linkIndexSortKey(""))
		if !ok {
			t.Fatal("no link index survived the race")
		}
		if got := numberAttr(index, "version"); got < 2 {
			t.Errorf("index version = %d, want a version per committed write — an unconditional put would sit at 1", got)
		}
		for _, l := range linkValues() {
			pk := naming.LinkVarsKey("shop", store.Class, l.Link)
			if len(ddb.items[pk]) == 0 {
				t.Errorf("%s was pruned away by a deploy that also publishes it", pk)
			}
		}
	})

	t.Run("gives up naming the race rather than pruning on stale reads", func(t *testing.T) {
		store, ddb, _ := newTestStore(t)
		if _, err := store.PublishLinks(context.Background(), "shop", "", linkValues()); err != nil {
			t.Fatalf("seed PublishLinks: %v", err)
		}

		var reentered bool
		ddb.beforePut = func() {
			if reentered {
				return
			}
			reentered = true
			defer func() { reentered = false }()
			ddb.beforePut = func() { ddb.beforePut = nil }
			if _, err := store.PublishLinks(context.Background(), "shop", "", linkValues()); err != nil {
				return
			}
		}

		if _, err := store.PublishLinks(context.Background(), "shop", "", linkValues()); err != nil && !strings.Contains(err.Error(), "racing") {
			t.Errorf("err = %v, want either success or a refusal naming the racing deploy", err)
		}
	})
}

func TestLinkCoordinatesAreNotUserEditable(t *testing.T) {
	store, _, _ := newTestStore(t)
	c := Coordinate{Slug: "shop", Link: "main", Key: "OCEL_RESOURCE_POSTGRES_main"}

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
	if _, err := store.PublishLinks(context.Background(), "shop", "", linkValues()); err != nil {
		t.Fatalf("PublishLinks: %v", err)
	}

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
	if _, err := store.PublishLinks(context.Background(), "shop", "", linkValues()); err != nil {
		t.Fatalf("PublishLinks: %v", err)
	}

	if _, err := store.Purge(context.Background(), "shop"); err != nil {
		t.Fatalf("Purge: %v", err)
	}
	for pk, sks := range ddb.items {
		if len(sks) > 0 {
			t.Errorf("%s still holds %d rows after the project was purged", pk, len(sks))
		}
	}
}

func linkCells(links []LinkValue, environment string) []Coordinate {
	cells := make([]Coordinate, 0, len(links))
	for _, l := range links {
		cells = append(cells, Coordinate{Slug: "shop", Link: l.Link, Key: l.Key, Environment: environment})
	}
	return cells
}

package vars

import (
	"context"
	"errors"
	"slices"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/ocelhq/ocel/pkg/naming"
	linksv1 "github.com/ocelhq/ocel/pkg/proto/links/v1"
)

const sstPublisher = "sst"

func sstPostgres(name, host string) *linksv1.Link {
	return &linksv1.Link{
		Name:   name,
		Source: sstPublisher,
		Properties: &linksv1.Link_Postgres{Postgres: &linksv1.PostgresProperties{
			Host: host, Port: 5432, Database: "orders", Username: "app", Password: "pw",
		}},
	}
}

func sstRecords() []*linksv1.Link {
	orders := sstPostgres("orders", "orders.cluster-abc.us-east-1.rds.amazonaws.com")
	orders.Grants = []*linksv1.Grant{{
		Actions:   []string{"rds-db:connect"},
		Resources: []string{"arn:aws:rds-db:us-east-1:1234:dbuser:db-ORDERS/app"},
		Label:     "connect",
	}}
	return []*linksv1.Link{
		orders,
		bucketLink("invoices", "acme-invoices", sstPublisher, &linksv1.Grant{
			Actions:   []string{"s3:GetObject"},
			Resources: []string{"arn:aws:s3:::acme-invoices/*"},
			Label:     "read",
		}),
	}
}

func linkOfType(t *testing.T, kind linksv1.LinkType, name, source string) *linksv1.Link {
	t.Helper()
	switch kind {
	case linksv1.LinkType_LINK_TYPE_POSTGRES:
		return postgresLink(name, "h", source)
	case linksv1.LinkType_LINK_TYPE_BUCKET:
		return bucketLink(name, "b", source)
	}
	t.Fatalf("no fixture builds a %v link", kind)
	return nil
}

func publishFor(t *testing.T, s *Store, publisher, environment string, records []*linksv1.Link) PublishResult {
	t.Helper()
	result, err := s.PublishFor(context.Background(), "shop", publisher, environment, records)
	if err != nil {
		t.Fatalf("PublishFor %q %q: %v", publisher, environment, err)
	}
	return result
}

func TestPublishForRoundTripsThroughTheSamePair(t *testing.T) {
	store, ddb, _ := newTestStore(t)
	want := sstRecords()

	publishFor(t, store, sstPublisher, "", want)

	for _, r := range want {
		pk := naming.LinkVarsKey("shop", store.Class, r.GetName())
		if len(ddb.items[pk]) != 2 {
			t.Errorf("%s holds %d rows, want the value row and the record row", pk, len(ddb.items[pk]))
		}
	}

	got := resolve(t, store, "", recordNames(want)...)
	if len(got) != len(want) {
		t.Fatalf("ResolveRecords returned %d records, want %d", len(got), len(want))
	}
	for i, p := range got {
		if p.Type() != naming.LinkTypeOf(want[i]) {
			t.Errorf("%s = type %v, want %v", p.Name(), p.Type(), naming.LinkTypeOf(want[i]))
		}
		if p.Link.GetSource() != sstPublisher {
			t.Errorf("%s = source %q, want %q", p.Name(), p.Link.GetSource(), sstPublisher)
		}
		if !proto.Equal(p.Link, want[i]) {
			t.Errorf("%s = %+v, want %+v", p.Name(), p.Link, want[i])
		}
	}
}

func TestPublishForRefusesAnUnsourcedLink(t *testing.T) {
	for _, kind := range naming.LinkTypes() {
		store, ddb, _ := newTestStore(t)

		_, err := store.PublishFor(context.Background(), "shop", sstPublisher, "", []*linksv1.Link{linkOfType(t, kind, "orders", "")})

		if !errors.Is(err, ErrUnsourced) {
			t.Errorf("PublishFor %v = %v, want ErrUnsourced", kind, err)
		}
		if len(ddb.transactions) != 0 {
			t.Errorf("PublishFor %v wrote %d transactions before refusing", kind, len(ddb.transactions))
		}

		publishFor(t, store, sstPublisher, "", []*linksv1.Link{linkOfType(t, kind, "orders", sstPublisher)})
		if got := resolve(t, store, "", "orders"); got[0].Link.GetSource() != sstPublisher {
			t.Errorf("PublishFor %v = source %q, want the publisher's own", kind, got[0].Link.GetSource())
		}
	}
}

func TestPublishForRefusesAnUnscopedGrant(t *testing.T) {
	store, _, _ := newTestStore(t)

	_, err := store.PublishFor(context.Background(), "shop", sstPublisher, "", []*linksv1.Link{
		bucketLink("orders", "acme-invoices", sstPublisher, &linksv1.Grant{Actions: []string{"s3:*"}, Resources: []string{"arn:aws:s3:::acme-invoices/*"}}),
	})

	if !errors.Is(err, ErrUnscopedGrant) {
		t.Fatalf("PublishFor = %v, want ErrUnscopedGrant", err)
	}
}

func TestPublishForNamesAnAnonymousPublisher(t *testing.T) {
	store, _, _ := newTestStore(t)

	for _, publisher := range []string{"", "sst#prod"} {
		if _, err := store.PublishFor(context.Background(), "shop", publisher, "", sstRecords()); err == nil {
			t.Errorf("PublishFor %q succeeded; a publisher owns its records by name", publisher)
		}
	}
}

func TestPruneForRefusesACoordinateItCannotAddress(t *testing.T) {
	store, ddb, _ := newTestStore(t)

	for name, coordinate := range map[string][2]string{
		"no slug":                            {"", ""},
		"slug with the key delimiter":        {"shop" + delimiter + "eu", ""},
		"environment with the key delimiter": {"shop", "pr" + delimiter + "9"},
		"the class-wide marker":              {"shop", ClassWideEnvironment},
	} {
		if _, err := store.PruneFor(context.Background(), coordinate[0], sstPublisher, coordinate[1]); err == nil {
			t.Errorf("PruneFor accepted %s; it would write an index row nothing can read back", name)
		}
	}
	if len(ddb.puts) != 0 {
		t.Errorf("PruneFor wrote %d rows before refusing", len(ddb.puts))
	}
}

func TestPruneForTakesEveryRecordItPublished(t *testing.T) {
	store, ddb, _ := newTestStore(t)
	published := sstRecords()

	publishFor(t, store, sstPublisher, "", published)
	publish(t, store, "", linkRecords())

	result, err := store.PruneFor(context.Background(), "shop", sstPublisher, "")
	if err != nil {
		t.Fatalf("PruneFor: %v", err)
	}
	if result.Pruned != 2*len(published) {
		t.Errorf("PruneFor removed %d rows, want %d", result.Pruned, 2*len(published))
	}

	for _, r := range published {
		pk := naming.LinkVarsKey("shop", store.Class, r.GetName())
		if len(ddb.items[pk]) != 0 {
			t.Errorf("%s still holds %d rows after the stack was destroyed", pk, len(ddb.items[pk]))
		}
		if _, err := store.ResolveRecords(context.Background(), "shop", "", []string{r.GetName()}); !errors.Is(err, ErrNotPublished) {
			t.Errorf("ResolveRecords %s = %v, want ErrNotPublished", r.GetName(), err)
		}
	}

	if got := resolve(t, store, "", recordNames(linkRecords())...); len(got) != len(linkRecords()) {
		t.Errorf("a destroy took %d of ocel's own links with it", len(linkRecords())-len(got))
	}
}

func TestPublishForPrunesWhatItStoppedPublishing(t *testing.T) {
	store, _, _ := newTestStore(t)

	publishFor(t, store, sstPublisher, "", sstRecords())
	publishFor(t, store, sstPublisher, "", sstRecords()[:1])

	if _, err := store.ResolveRecords(context.Background(), "shop", "", []string{"invoices"}); !errors.Is(err, ErrNotPublished) {
		t.Errorf("ResolveRecords invoices = %v, want ErrNotPublished", err)
	}
	if got := resolve(t, store, "", "orders"); got[0].Link.GetSource() != sstPublisher {
		t.Errorf("orders = source %q, want it left alone", got[0].Link.GetSource())
	}
}

func TestAnOcelDeployLeavesAPublishedLinkStanding(t *testing.T) {
	store, _, _ := newTestStore(t)

	publishFor(t, store, sstPublisher, "", sstRecords())
	publish(t, store, "", linkRecords())
	publish(t, store, "", linkRecords()[:1])

	if got := resolve(t, store, "", recordNames(sstRecords())...); len(got) != len(sstRecords()) {
		t.Fatalf("an ocel deploy pruned %d of sst's links", len(sstRecords())-len(got))
	}
}

func TestOneLinkNameBelongsToOnePublisher(t *testing.T) {
	t.Run("a publisher may not take a link ocel provisions", func(t *testing.T) {
		store, _, _ := newTestStore(t)
		publish(t, store, "", linkRecords())

		_, err := store.PublishFor(context.Background(), "shop", sstPublisher, "", []*linksv1.Link{sstPostgres("main", "h")})
		if !errors.Is(err, ErrClaimed) {
			t.Fatalf("PublishFor = %v, want ErrClaimed", err)
		}
	})

	t.Run("a publisher may not take another publisher's link", func(t *testing.T) {
		store, _, _ := newTestStore(t)
		publishFor(t, store, sstPublisher, "", sstRecords())

		_, err := store.PublishFor(context.Background(), "shop", "pulumi", "", []*linksv1.Link{postgresLink("orders", "h", "pulumi")})
		if !errors.Is(err, ErrClaimed) {
			t.Fatalf("PublishFor = %v, want ErrClaimed", err)
		}
	})

	t.Run("an ocel deploy shadows a published link rather than aborting mid-deploy", func(t *testing.T) {
		store, _, _ := newTestStore(t)
		publishFor(t, store, sstPublisher, "", []*linksv1.Link{sstPostgres("main", "published.example")})

		if _, err := store.PublishRecords(context.Background(), "shop", "", OwnerOcel, linkRecords()); err != nil {
			t.Fatalf("PublishRecords = %v, want the resources this deploy provisioned delivered", err)
		}

		got := resolve(t, store, "", "main")
		if got[0].Link.GetSource() != "" || got[0].Link.GetPostgres().GetHost() != mainHost {
			t.Errorf("main = %+v, want the resource ocel provisioned to own the name it provisioned", got[0].Link)
		}

		_, err := store.PublishFor(context.Background(), "shop", sstPublisher, "", []*linksv1.Link{sstPostgres("main", "published.example")})
		if !errors.Is(err, ErrClaimed) {
			t.Fatalf("PublishFor = %v, want the publisher refused once ocel provisions that name", err)
		}
	})

	t.Run("the claim is taken by the write, not by a check that ran before it", func(t *testing.T) {
		store, ddb, _ := newTestStore(t)
		ddb.armBeforePutTo(linkIndexSortKey("pulumi", ""), func() {
			publishFor(t, store, sstPublisher, "", sstRecords()[:1])
		})

		_, err := store.PublishFor(context.Background(), "shop", "pulumi", "", []*linksv1.Link{postgresLink("orders", "pulumi.example", "pulumi")})
		if !errors.Is(err, ErrClaimed) {
			t.Fatalf("PublishFor = %v, want ErrClaimed: the name was taken between this publish's check and its write", err)
		}
		if got := resolve(t, store, "", "orders"); got[0].Link.GetSource() != sstPublisher {
			t.Errorf("orders = source %q, want the publisher that took the name first to still hold it", got[0].Link.GetSource())
		}
	})

	t.Run("a publisher's prune leaves a link another publisher holds alone", func(t *testing.T) {
		store, ddb, _ := newTestStore(t)
		ddb.armBeforePutTo(linkIndexSortKey("pulumi", ""), func() {
			publishFor(t, store, sstPublisher, "", sstRecords()[:1])
		})
		if _, err := store.PublishFor(context.Background(), "shop", "pulumi", "", []*linksv1.Link{postgresLink("orders", "pulumi.example", "pulumi")}); !errors.Is(err, ErrClaimed) {
			t.Fatalf("PublishFor = %v, want ErrClaimed", err)
		}

		if _, err := store.PruneFor(context.Background(), "shop", "pulumi", ""); err != nil {
			t.Fatalf("PruneFor: %v", err)
		}
		if got := resolve(t, store, "", "orders"); got[0].Link.GetSource() != sstPublisher {
			t.Errorf("orders = source %q, want a destroy to take only what its own publisher wrote", got[0].Link.GetSource())
		}
	})

	t.Run("a named environment may not shadow what another publisher binds class-wide", func(t *testing.T) {
		store, _, _ := newTestStore(t)
		publish(t, store, "", linkRecords())

		_, err := store.PublishFor(context.Background(), "shop", sstPublisher, "pr-9", []*linksv1.Link{sstPostgres("main", "h")})
		if !errors.Is(err, ErrClaimed) {
			t.Fatalf("PublishFor = %v, want ErrClaimed", err)
		}
	})

	t.Run("republishing its own link is not a collision", func(t *testing.T) {
		store, _, _ := newTestStore(t)
		publishFor(t, store, sstPublisher, "", sstRecords())
		publishFor(t, store, sstPublisher, "", sstRecords())
	})
}

func TestPublishForShadowsTheClassWidePair(t *testing.T) {
	store, _, _ := newTestStore(t)

	classWide := sstRecords()[:1]
	publishFor(t, store, sstPublisher, "", classWide)

	named := []*linksv1.Link{sstPostgres("orders", "pr-9.cluster-abc.us-east-1.rds.amazonaws.com")}
	publishFor(t, store, sstPublisher, "pr-9", named)

	if got := resolve(t, store, "pr-9", "orders"); got[0].Link.GetPostgres().GetHost() != named[0].GetPostgres().GetHost() {
		t.Errorf("pr-9 read host %q, want the named pair's %q", got[0].Link.GetPostgres().GetHost(), named[0].GetPostgres().GetHost())
	}
	if got := resolve(t, store, "pr-9", "orders"); len(got[0].Link.GetGrants()) != 0 {
		t.Errorf("the named pair was merged cell-by-cell with the class-wide one")
	}
	if got := resolve(t, store, "", "orders"); got[0].Link.GetPostgres().GetHost() != classWide[0].GetPostgres().GetHost() {
		t.Errorf("the class-wide read saw host %q, want %q", got[0].Link.GetPostgres().GetHost(), classWide[0].GetPostgres().GetHost())
	}
}

func TestPruneForLeavesASiblingEnvironmentAlone(t *testing.T) {
	store, _, _ := newTestStore(t)

	publishFor(t, store, sstPublisher, "", sstRecords()[:1])
	publishFor(t, store, sstPublisher, "pr-9", sstRecords()[:1])

	if _, err := store.PruneFor(context.Background(), "shop", sstPublisher, "pr-9"); err != nil {
		t.Fatalf("PruneFor pr-9: %v", err)
	}

	if got := resolve(t, store, "", "orders"); got[0].Name() != "orders" {
		t.Errorf("tearing down pr-9 took the class-wide pair with it")
	}
}

func TestACompositePublishesOneRecordPerConsumableResource(t *testing.T) {
	store, ddb, _ := newTestStore(t)

	composite := []*linksv1.Link{
		sstPostgres("orders", "h"),
		bucketLink("orders-attachments", "acme-orders-attachments", sstPublisher),
	}

	publishFor(t, store, sstPublisher, "", composite)

	for _, r := range composite {
		pk := naming.LinkVarsKey("shop", store.Class, r.GetName())
		if len(ddb.items[pk]) != 2 {
			t.Errorf("%s holds %d rows, want exactly the one pair", pk, len(ddb.items[pk]))
		}
	}

	index, ok := ddb.get(PartitionKey("shop", store.Class), linkIndexSortKey(sstPublisher, ""))
	if !ok {
		t.Fatalf("the publisher recorded no index; nothing can prune what it published")
	}
	names, err := unmarshal(index)
	if err != nil {
		t.Fatalf("unmarshal index: %v", err)
	}
	slices.Sort(names.Names)
	if !slices.Equal(names.Names, []string{"orders", "orders-attachments"}) {
		t.Errorf("the publisher owns %v, want one name per consumable resource and no constituents", names.Names)
	}
}

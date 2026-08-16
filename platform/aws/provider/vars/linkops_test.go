package vars

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	linksv1 "github.com/ocelhq/ocel/pkg/proto/links/v1"
)

func setLink(t *testing.T, s *Store, owner, environment string, link *linksv1.Link) int64 {
	t.Helper()
	version, err := s.SetLink(context.Background(), "shop", owner, environment, link)
	if err != nil {
		t.Fatalf("SetLink %q %q: %v", owner, link.GetName(), err)
	}
	return version
}

func TestSetLinkLeavesThePublisherSOtherLinksAlone(t *testing.T) {
	store, _, _ := newTestStore(t)
	publishFor(t, store, sstPublisher, "", sstRecords())

	setLink(t, store, sstPublisher, "", bucketLink("uploads", "shop-uploads", sstPublisher))

	names, err := store.PublishedNames(context.Background(), "shop", store.Class, "")
	if err != nil {
		t.Fatalf("PublishedNames: %v", err)
	}
	for _, want := range []string{"invoices", "orders", "uploads"} {
		if !slices.Contains(names, want) {
			t.Errorf("PublishedNames = %v, want %s still bound", names, want)
		}
	}
	if got := resolve(t, store, "", "orders"); got[0].Link.GetPostgres().GetHost() == "" {
		t.Error("SetLink pruned the value beside a link it never named")
	}
}

func TestSetLinkRefusesAnotherPublisherSName(t *testing.T) {
	store, _, _ := newTestStore(t)
	publishFor(t, store, sstPublisher, "", sstRecords())

	_, err := store.SetLink(context.Background(), "shop", "terraform", "", postgresLink("orders", mainHost, "terraform"))

	if !errors.Is(err, ErrClaimed) {
		t.Fatalf("SetLink = %v, want ErrClaimed", err)
	}
	for _, owner := range []string{sstPublisher, "terraform"} {
		if !strings.Contains(err.Error(), owner) {
			t.Errorf("SetLink said %q, which never names %s", err, owner)
		}
	}
}

func TestSetLinkOverItsOwnRecordBumpsTheVersion(t *testing.T) {
	store, _, _ := newTestStore(t)

	first := setLink(t, store, sstPublisher, "", postgresLink("orders", mainHost, sstPublisher))
	second := setLink(t, store, sstPublisher, "", postgresLink("orders", "moved.host.example", sstPublisher))

	if second <= first {
		t.Errorf("SetLink twice = versions %d then %d, want the second higher", first, second)
	}
	if got := resolve(t, store, "", "orders"); got[0].Link.GetPostgres().GetHost() != "moved.host.example" {
		t.Errorf("SetLink left host %q, want the one it just wrote", got[0].Link.GetPostgres().GetHost())
	}
}

func TestSetLinkRefusesWhatTheStoreCannotBind(t *testing.T) {
	store, ddb, _ := newTestStore(t)

	for name, tc := range map[string]struct {
		owner string
		link  *linksv1.Link
	}{
		"an unsourced link":   {sstPublisher, postgresLink("orders", mainHost, "")},
		"ocel's own name":     {OwnerOcel, postgresLink("orders", mainHost, sstPublisher)},
		"a link with no name": {sstPublisher, postgresLink("", mainHost, sstPublisher)},
		"an unscoped grant": {sstPublisher, bucketLink("orders", "b", sstPublisher, &linksv1.Grant{
			Actions:   []string{"s3:*"},
			Resources: []string{"arn:aws:s3:::b/*"},
		})},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := store.SetLink(context.Background(), "shop", tc.owner, "", tc.link); err == nil {
				t.Fatalf("SetLink accepted %s", name)
			}
		})
	}
	if len(ddb.transactions) != 0 {
		t.Errorf("SetLink wrote %d transactions before refusing", len(ddb.transactions))
	}
}

func TestRemoveLinkTakesOutAnotherPublisherSLink(t *testing.T) {
	store, ddb, _ := newTestStore(t)
	publishFor(t, store, sstPublisher, "", sstRecords())

	removed, err := store.RemoveLink(context.Background(), "shop", "", "orders")
	if err != nil {
		t.Fatalf("RemoveLink: %v", err)
	}
	if !removed {
		t.Fatal("RemoveLink = false, want the link the CLI named gone")
	}

	if got := len(ddb.items[LinkPartitionKey("shop", store.Class, "orders")]); got != 0 {
		t.Errorf("orders holds %d rows after RemoveLink, want none", got)
	}
	names, err := store.PublishedNames(context.Background(), "shop", store.Class, "")
	if err != nil {
		t.Fatalf("PublishedNames: %v", err)
	}
	if slices.Contains(names, "orders") {
		t.Errorf("PublishedNames = %v, want orders unbound", names)
	}
	if !slices.Contains(names, "invoices") {
		t.Errorf("PublishedNames = %v, want the publisher's other link left alone", names)
	}
}

func TestRemoveLinkOnANameNobodyPublishes(t *testing.T) {
	store, ddb, _ := newTestStore(t)
	publishFor(t, store, sstPublisher, "", sstRecords())
	before := len(ddb.transactions)

	removed, err := store.RemoveLink(context.Background(), "shop", "", "nothing-here")
	if err != nil {
		t.Fatalf("RemoveLink: %v", err)
	}
	if removed {
		t.Error("RemoveLink = true for a name nothing published")
	}
	if len(ddb.transactions) != before {
		t.Errorf("RemoveLink wrote %d transactions for a name nothing published", len(ddb.transactions)-before)
	}
}

func TestListLinksReportsWhatBindsWithoutOpeningIt(t *testing.T) {
	store, _, crypto := newTestStore(t)
	publishFor(t, store, sstPublisher, "", sstRecords())
	publish(t, store, "", []*linksv1.Link{postgresLink("main", mainHost, "")})
	before := crypto.decrypts

	got, err := store.ListLinks(context.Background(), "shop", "")
	if err != nil {
		t.Fatalf("ListLinks: %v", err)
	}
	if crypto.decrypts != before {
		t.Errorf("ListLinks decrypted %d values; a listing never opens one", crypto.decrypts-before)
	}

	if len(got) != 3 {
		t.Fatalf("ListLinks = %d links, want invoices, main and orders", len(got))
	}
	if got[0].Name != "invoices" || got[1].Name != "main" || got[2].Name != "orders" {
		t.Fatalf("ListLinks = %+v, want them sorted by name", got)
	}

	orders := got[2]
	if orders.Type != linksv1.LinkType_LINK_TYPE_POSTGRES {
		t.Errorf("orders = type %v, want postgres", orders.Type)
	}
	if orders.Source != sstPublisher || orders.Owner != sstPublisher {
		t.Errorf("orders = source %q owner %q, want %q for both", orders.Source, orders.Owner, sstPublisher)
	}
	if orders.Version == 0 {
		t.Error("orders carries no version")
	}
	if got[1].Owner != OwnerOcel {
		t.Errorf("main = owner %q, want ocel's own", got[1].Owner)
	}

	for _, summary := range got {
		if strings.Contains(summary.Source, mainPassword) || strings.Contains(summary.Name, mainPassword) {
			t.Errorf("ListLinks carried a property value out: %+v", summary)
		}
	}
}

func TestListLinksPrefersTheEnvironmentSOwnRecord(t *testing.T) {
	store, _, _ := newTestStore(t)
	setLink(t, store, sstPublisher, "", postgresLink("orders", mainHost, sstPublisher))
	setLink(t, store, sstPublisher, "pr-9", bucketLink("orders", "pr-9-orders", "terraform"))

	got, err := store.ListLinks(context.Background(), "shop", "pr-9")
	if err != nil {
		t.Fatalf("ListLinks: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ListLinks = %+v, want one link", got)
	}
	if got[0].Type != linksv1.LinkType_LINK_TYPE_BUCKET || got[0].Source != "terraform" {
		t.Errorf("ListLinks = %+v, want the record published to pr-9 to shadow the class-wide one", got[0])
	}
}

package values_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit/fake"
	"github.com/ocelhq/ocel/pkg/providerkit/ports"
	"github.com/ocelhq/ocel/pkg/providerkit/values"
)

func fixture() (values.Store, values.Scope) {
	return values.Store{Records: fake.NewRecords(), Sealer: fake.NewSealer()},
		values.Scope{Project: "shop", Class: ports.ClassProduction}
}

func at(key string) values.Coordinate {
	return values.Coordinate{Cell: values.Cell{Key: key}}
}

func TestAValueIsSealedAndComesBackVersioned(t *testing.T) {
	store, scope := fixture()
	ctx := context.Background()

	first, err := store.Set(ctx, scope, at("DATABASE_URL"), "postgres://one", nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.Version != 1 || first.Size != int64(len("postgres://one")) {
		t.Fatalf("first Set() = %+v, want version 1 sized to the plaintext", first)
	}

	held, err := store.Get(ctx, scope, at("DATABASE_URL"), false)
	if err != nil {
		t.Fatal(err)
	}
	if held.Plaintext != "" {
		t.Fatal("Get() without reveal returned the plaintext")
	}

	revealed, err := store.Get(ctx, scope, at("DATABASE_URL"), true)
	if err != nil || revealed.Plaintext != "postgres://one" {
		t.Fatalf("Get() with reveal = %q, %v", revealed.Plaintext, err)
	}
}

func TestAWriteAtTheVersionSeenWinsAndAStaleOneLoses(t *testing.T) {
	store, scope := fixture()
	ctx := context.Background()

	if _, err := store.Set(ctx, scope, at("KEY"), "one", nil); err != nil {
		t.Fatal(err)
	}
	seen := int64(1)
	if _, err := store.Set(ctx, scope, at("KEY"), "two", &seen); err != nil {
		t.Fatalf("Set() at the version seen = %v, want it written", err)
	}
	if _, err := store.Set(ctx, scope, at("KEY"), "three", &seen); !errors.Is(err, values.ErrStaleVersion) {
		t.Fatalf("Set() at a version that moved = %v, want ErrStaleVersion", err)
	}
}

func TestADeletedValueIsGoneButItsVersionsSurvive(t *testing.T) {
	store, scope := fixture()
	ctx := context.Background()

	for _, value := range []string{"one", "two", "three"} {
		if _, err := store.Set(ctx, scope, at("KEY"), value, nil); err != nil {
			t.Fatal(err)
		}
	}
	deleted, err := store.Delete(ctx, scope, at("KEY"), nil)
	if err != nil || !deleted {
		t.Fatalf("Delete() = %v, %v", deleted, err)
	}
	if _, err := store.Get(ctx, scope, at("KEY"), false); !errors.Is(err, values.ErrNotFound) {
		t.Fatalf("Get() after Delete() = %v, want ErrNotFound", err)
	}
	held, err := store.List(ctx, scope)
	if err != nil || len(held) != 0 {
		t.Fatalf("List() after Delete() = %d values, %v, want the tombstone hidden", len(held), err)
	}

	versions, err := store.Versions(ctx, scope, at("KEY"))
	if err != nil || len(versions) != 3 {
		t.Fatalf("Versions() = %d entries, %v, want one per write", len(versions), err)
	}
	if versions[0].Version != 1 || versions[2].Version != 3 {
		t.Fatalf("Versions() = %+v, want them oldest first", versions)
	}

	again, err := store.Delete(ctx, scope, at("KEY"), nil)
	if err != nil || again {
		t.Fatalf("a second Delete() = %v, %v, want it report nothing removed", again, err)
	}
	written, err := store.Set(ctx, scope, at("KEY"), "four", nil)
	if err != nil || written.Version != 4 {
		t.Fatalf("Set() after Delete() = %+v, %v, want the version to carry on", written, err)
	}
}

func TestAValueOverTheCapIsRefused(t *testing.T) {
	store, scope := fixture()
	ctx := context.Background()

	if _, err := store.Set(ctx, scope, at("KEY"), strings.Repeat("x", values.MaxValueBytes), nil); err != nil {
		t.Fatalf("Set() at the cap = %v, want it written", err)
	}
	_, err := store.Set(ctx, scope, at("KEY"), strings.Repeat("x", values.MaxValueBytes+1), nil)
	if !errors.Is(err, values.ErrTooLarge) {
		t.Fatalf("Set() over the cap = %v, want ErrTooLarge", err)
	}
}

func TestAnEnvironmentValueShadowsTheClassWideOne(t *testing.T) {
	store, scope := fixture()
	ctx := context.Background()

	if _, err := store.Set(ctx, scope, at("KEY"), "class-wide", nil); err != nil {
		t.Fatal(err)
	}
	scoped := values.Coordinate{Cell: values.Cell{Key: "KEY"}, Environment: "pr-7"}
	if _, err := store.Set(ctx, scope, scoped, "pr-7 only", nil); err != nil {
		t.Fatal(err)
	}

	reader := values.Reader{Records: store.Records, Sealer: store.Sealer, Scope: scope, Environment: "pr-7"}
	seen, err := reader.Values(ctx, []values.Cell{{Key: "KEY"}})
	if err != nil || seen["KEY"] != "pr-7 only" {
		t.Fatalf("the environment's reader saw %q, %v, want the environment's own value", seen["KEY"], err)
	}

	classWide := values.Reader{Records: store.Records, Sealer: store.Sealer, Scope: scope}
	seen, err = classWide.Values(ctx, []values.Cell{{Key: "KEY"}})
	if err != nil || seen["KEY"] != "class-wide" {
		t.Fatalf("the class-wide reader saw %q, %v", seen["KEY"], err)
	}
}

func TestAReferenceResolvesOneHopAndNoFurther(t *testing.T) {
	store, scope := fixture()
	ctx := context.Background()
	shared := values.Scope{Project: "platform", Class: ports.ClassProduction}

	if _, err := store.Set(ctx, shared, at("DATABASE_URL"), "postgres://shared", nil); err != nil {
		t.Fatal(err)
	}
	target := values.Target{Project: "platform", Cell: values.Cell{Key: "DATABASE_URL"}}
	if _, err := store.SetReference(ctx, scope, at("DATABASE_URL"), target); err != nil {
		t.Fatal(err)
	}

	revealed, err := store.Get(ctx, scope, at("DATABASE_URL"), true)
	if err != nil || revealed.Plaintext != "postgres://shared" {
		t.Fatalf("Get() through a reference = %q, %v", revealed.Plaintext, err)
	}
	if revealed.Target == nil || revealed.Target.Project != "platform" {
		t.Fatalf("the reference's metadata names %v, want the target it points at", revealed.Target)
	}

	deeper := values.Scope{Project: "web", Class: ports.ClassProduction}
	_, err = store.SetReference(ctx, deeper, at("DATABASE_URL"), values.Target{Project: "shop", Cell: values.Cell{Key: "DATABASE_URL"}})
	if !errors.Is(err, values.ErrWouldDeepen) {
		t.Fatalf("a reference to a reference = %v, want ErrWouldDeepen", err)
	}
}

func TestAReferenceRefusesToShadowAValueItsOwnConsumersRead(t *testing.T) {
	store, scope := fixture()
	ctx := context.Background()
	shared := values.Scope{Project: "platform", Class: ports.ClassProduction}
	consumer := values.Scope{Project: "web", Class: ports.ClassProduction}

	if _, err := store.Set(ctx, scope, at("KEY"), "held here", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetReference(ctx, consumer, at("KEY"), values.Target{Project: "shop", Cell: values.Cell{Key: "KEY"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Set(ctx, shared, at("KEY"), "held elsewhere", nil); err != nil {
		t.Fatal(err)
	}

	_, err := store.SetReference(ctx, scope, at("KEY"), values.Target{Project: "platform", Cell: values.Cell{Key: "KEY"}})
	if !errors.Is(err, values.ErrWouldDeepen) {
		t.Fatalf("making a referenced cell a reference = %v, want ErrWouldDeepen", err)
	}
	if !strings.Contains(err.Error(), "web") {
		t.Fatalf("the refusal does not name the consumer: %v", err)
	}
}

func TestSettingAValueOverAReferenceIsRefused(t *testing.T) {
	store, scope := fixture()
	ctx := context.Background()
	shared := values.Scope{Project: "platform", Class: ports.ClassProduction}

	if _, err := store.Set(ctx, shared, at("KEY"), "held elsewhere", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetReference(ctx, scope, at("KEY"), values.Target{Project: "platform", Cell: values.Cell{Key: "KEY"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Set(ctx, scope, at("KEY"), "held here now", nil); !errors.Is(err, values.ErrIsReference) {
		t.Fatalf("Set() over a reference = %v, want ErrIsReference", err)
	}
}

func TestTheReverseIndexAnswersWhoReadsACell(t *testing.T) {
	store, scope := fixture()
	ctx := context.Background()

	if _, err := store.Set(ctx, scope, at("KEY"), "held here", nil); err != nil {
		t.Fatal(err)
	}
	target := values.Target{Project: "shop", Cell: values.Cell{Key: "KEY"}}
	for _, project := range []string{"web", "worker"} {
		consumer := values.Scope{Project: project, Class: ports.ClassProduction}
		if _, err := store.SetReference(ctx, consumer, at("KEY"), target); err != nil {
			t.Fatal(err)
		}
	}

	found, err := store.References(ctx, scope, at("KEY"))
	if err != nil || len(found) != 2 {
		t.Fatalf("References() = %+v, %v, want both consumers", found, err)
	}
	if found[0].Project != "web" || found[1].Project != "worker" {
		t.Fatalf("References() = %+v, want them sorted", found)
	}

	consumer := values.Scope{Project: "web", Class: ports.ClassProduction}
	if _, err := store.Delete(ctx, consumer, at("KEY"), nil); err != nil {
		t.Fatal(err)
	}
	found, err = store.References(ctx, scope, at("KEY"))
	if err != nil || len(found) != 1 || found[0].Project != "worker" {
		t.Fatalf("References() after a consumer deleted its reference = %+v, %v", found, err)
	}
}

func TestRevealAnswersOnlyTheCellsThatHoldValues(t *testing.T) {
	store, scope := fixture()
	ctx := context.Background()

	if _, err := store.Set(ctx, scope, at("A"), "one", nil); err != nil {
		t.Fatal(err)
	}
	found, err := store.Reveal(ctx, scope, []values.Coordinate{at("A"), at("B")})
	if err != nil || len(found) != 1 || found[0].Plaintext != "one" {
		t.Fatalf("Reveal() = %+v, %v, want only the cell that holds a value", found, err)
	}
}

func TestARevealOverABrokenReferenceFailsRatherThanOmittingIt(t *testing.T) {
	store, scope := fixture()
	ctx := context.Background()
	shared := values.Scope{Project: "platform", Class: ports.ClassProduction}

	if _, err := store.Set(ctx, shared, at("DATABASE_URL"), "postgres://shared", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetReference(ctx, scope, at("DATABASE_URL"), values.Target{Project: "platform", Cell: values.Cell{Key: "DATABASE_URL"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Delete(ctx, shared, at("DATABASE_URL"), nil); err != nil {
		t.Fatal(err)
	}

	_, err := store.Reveal(ctx, scope, []values.Coordinate{at("DATABASE_URL")})
	if !errors.Is(err, values.ErrDangling) {
		t.Fatalf("Reveal() over a reference to a value that is gone = %v, want ErrDangling rather than a quietly missing variable", err)
	}
	if !strings.Contains(err.Error(), "platform") {
		t.Fatalf("the failure does not name the broken reference: %v", err)
	}

	reader := values.Reader{Records: store.Records, Sealer: store.Sealer, Scope: scope}
	if _, err := reader.Values(ctx, []values.Cell{{Key: "DATABASE_URL"}}); !errors.Is(err, values.ErrDangling) {
		t.Fatalf("a reader over a broken reference = %v, want it to refuse to boot the app", err)
	}
}

func TestPurgeFreesTheCellsTheProjectWasReading(t *testing.T) {
	store, scope := fixture()
	ctx := context.Background()
	consumer := values.Scope{Project: "web", Class: ports.ClassProduction}

	if _, err := store.Set(ctx, scope, at("KEY"), "held here", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetReference(ctx, consumer, at("KEY"), values.Target{Project: "shop", Cell: values.Cell{Key: "KEY"}}); err != nil {
		t.Fatal(err)
	}

	if _, err := store.Purge(ctx, consumer); err != nil {
		t.Fatal(err)
	}
	found, err := store.References(ctx, scope, at("KEY"))
	if err != nil || len(found) != 0 {
		t.Fatalf("References() after the consuming project was purged = %+v, %v, want nothing reading it", found, err)
	}

	shared := values.Scope{Project: "platform", Class: ports.ClassProduction}
	if _, err := store.Set(ctx, shared, at("KEY"), "held elsewhere", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetReference(ctx, scope, at("KEY"), values.Target{Project: "platform", Cell: values.Cell{Key: "KEY"}}); err != nil {
		t.Fatalf("SetReference() over a cell only a purged project read = %v, want it taken", err)
	}
}

func TestPurgeTakesEveryRecordAProjectHolds(t *testing.T) {
	store, scope := fixture()
	ctx := context.Background()

	if _, err := store.Set(ctx, scope, at("KEY"), "one", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetLink(ctx, scope, "", "OCEL", "db", values.Pair{Record: []byte("{}"), Value: []byte("{}")}); err != nil {
		t.Fatal(err)
	}
	consumer := values.Scope{Project: "web", Class: ports.ClassProduction}
	if _, err := store.SetReference(ctx, consumer, at("KEY"), values.Target{Project: "shop", Cell: values.Cell{Key: "KEY"}}); err != nil {
		t.Fatal(err)
	}

	if _, err := store.Purge(ctx, scope); err != nil {
		t.Fatal(err)
	}
	held, err := store.List(ctx, scope)
	if err != nil || len(held) != 0 {
		t.Fatalf("List() after Purge() = %d values, %v", len(held), err)
	}
	names, err := store.PublishedNames(ctx, scope, "")
	if err != nil || len(names) != 0 {
		t.Fatalf("PublishedNames() after Purge() = %v, %v", names, err)
	}
	found, err := store.References(ctx, scope, at("KEY"))
	if err != nil || len(found) != 0 {
		t.Fatalf("References() after Purge() = %+v, %v", found, err)
	}
}

func TestAKeyKeepsItsShapeThroughTheRecordTree(t *testing.T) {
	store, scope := fixture()
	ctx := context.Background()

	awkward := []values.Coordinate{
		{Cell: values.Cell{Key: "A/B"}},
		{Cell: values.Cell{Key: "A%2FB"}},
		{Cell: values.Cell{Folder: "/apps/web", Key: "KEY"}},
		{Cell: values.Cell{Folder: "/apps/web/inner", Key: "KEY"}},
		{Cell: values.Cell{Key: "KEY"}, Environment: "pr/7"},
	}
	for i, cell := range awkward {
		if _, err := store.Set(ctx, scope, cell, string(rune('a'+i)), nil); err != nil {
			t.Fatalf("Set(%+v) = %v", cell, err)
		}
	}
	for i, cell := range awkward {
		held, err := store.Get(ctx, scope, cell, true)
		if err != nil || held.Plaintext != string(rune('a'+i)) {
			t.Fatalf("Get(%+v) = %q, %v, want the value written there and no other", cell, held.Plaintext, err)
		}
	}
	held, err := store.List(ctx, scope)
	if err != nil || len(held) != len(awkward) {
		t.Fatalf("List() = %d values, %v, want the %d written", len(held), err, len(awkward))
	}
	for _, m := range held {
		if m.Coordinate.Key == "" {
			t.Fatalf("List() returned a value whose coordinate did not survive the round trip: %+v", m)
		}
	}
}

func TestALinkNameBelongsToOnePublisher(t *testing.T) {
	store, scope := fixture()
	ctx := context.Background()
	pair := values.Pair{Record: []byte("{}"), Value: []byte(`{"name":"db"}`)}

	if _, err := store.SetLink(ctx, scope, "", "neon", "db", pair); err != nil {
		t.Fatal(err)
	}
	_, err := store.SetLink(ctx, scope, "", "supabase", "db", pair)
	if !errors.Is(err, values.ErrClaimed) {
		t.Fatalf("a second publisher taking the name = %v, want ErrClaimed", err)
	}
	if !strings.Contains(err.Error(), "neon") {
		t.Fatalf("the refusal does not name the holder: %v", err)
	}

	version, err := store.SetLink(ctx, scope, "", "neon", "db", pair)
	if err != nil || version != 2 {
		t.Fatalf("the holder republishing = %d, %v, want version 2", version, err)
	}
}

func TestALinkIsRemovedAndItsNameFreed(t *testing.T) {
	store, scope := fixture()
	ctx := context.Background()
	pair := values.Pair{Record: []byte("{}"), Value: []byte(`{"name":"db"}`)}

	if _, err := store.SetLink(ctx, scope, "", "neon", "db", pair); err != nil {
		t.Fatal(err)
	}
	removed, err := store.RemoveLink(ctx, scope, "", "db")
	if err != nil || !removed {
		t.Fatalf("RemoveLink() = %v, %v", removed, err)
	}
	if again, err := store.RemoveLink(ctx, scope, "", "db"); err != nil || again {
		t.Fatalf("a second RemoveLink() = %v, %v, want it report nothing removed", again, err)
	}
	if _, err := store.SetLink(ctx, scope, "", "supabase", "db", pair); err != nil {
		t.Fatalf("publishing to the freed name = %v, want it allowed", err)
	}
}

func TestALinkPublishedToAnEnvironmentShadowsTheClassWidePair(t *testing.T) {
	store, scope := fixture()
	ctx := context.Background()

	if _, err := store.SetLink(ctx, scope, "", "OCEL", "db", values.Pair{Record: []byte(`{"class":true}`), Value: []byte("class-wide")}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetLink(ctx, scope, "pr-7", "OCEL", "db", values.Pair{Record: []byte(`{"env":true}`), Value: []byte("pr-7 only")}); err != nil {
		t.Fatal(err)
	}

	published, err := store.ResolveLink(ctx, scope, "pr-7", "db")
	if err != nil || string(published.Value) != "pr-7 only" {
		t.Fatalf("ResolveLink() in the environment = %q, %v", published.Value, err)
	}
	published, err = store.ResolveLink(ctx, scope, "", "db")
	if err != nil || string(published.Value) != "class-wide" {
		t.Fatalf("ResolveLink() class-wide = %q, %v", published.Value, err)
	}

	summaries, err := store.ListLinks(ctx, scope, "pr-7")
	if err != nil || len(summaries) != 1 || string(summaries[0].Record) != `{"env":true}` {
		t.Fatalf("ListLinks() in the environment = %+v, %v, want the environment's own record", summaries, err)
	}
}

func TestAReaderResolvesTheLinksADeploymentWasBuiltToRead(t *testing.T) {
	store, scope := fixture()
	ctx := context.Background()

	if _, err := store.SetLink(ctx, scope, "", "OCEL", "db", values.Pair{Record: []byte("{}"), Value: []byte("the sealed record")}); err != nil {
		t.Fatal(err)
	}
	reader := values.Reader{Records: store.Records, Sealer: store.Sealer, Scope: scope}

	found, err := reader.Links(ctx, []string{"db"})
	if err != nil || len(found) != 1 || string(found[0].Value) != "the sealed record" {
		t.Fatalf("Links() = %+v, %v", found, err)
	}
	if _, err := reader.Links(ctx, []string{"db", "cache"}); !errors.Is(err, values.ErrNotPublished) {
		t.Fatalf("Links() naming a link nobody published = %v, want ErrNotPublished", err)
	}
}

func TestReferenceOwnersNamesTheProjectsAValueIsBorrowedFrom(t *testing.T) {
	store, scope := fixture()
	ctx := context.Background()
	shared := values.Scope{Project: "platform", Class: ports.ClassProduction}

	if _, err := store.Set(ctx, shared, at("DATABASE_URL"), "postgres://shared", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Set(ctx, scope, at("OWN"), "held here", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetReference(ctx, scope, at("DATABASE_URL"), values.Target{Project: "platform", Cell: values.Cell{Key: "DATABASE_URL"}}); err != nil {
		t.Fatal(err)
	}

	owners, err := store.ReferenceOwners(ctx, scope)
	if err != nil || len(owners) != 1 {
		t.Fatalf("ReferenceOwners() = %v, %v, want only the borrowed cell", owners, err)
	}
	if owners[at("DATABASE_URL")] != "platform" {
		t.Fatalf("ReferenceOwners() = %v, want the borrowed cell to name the project holding it", owners)
	}
}

func TestAnUnpublishedLinkIsNotFound(t *testing.T) {
	store, scope := fixture()

	_, err := store.ResolveLink(context.Background(), scope, "", "db")
	if !errors.Is(err, values.ErrNotPublished) {
		t.Fatalf("ResolveLink() of a link nobody published = %v, want ErrNotPublished", err)
	}
}

func TestTheClassWideEnvironmentIsReserved(t *testing.T) {
	store, scope := fixture()
	ctx := context.Background()

	if err := values.ValidateLinkEnvironment("*"); err == nil {
		t.Fatal("the class-wide token was accepted as an environment name")
	}
	if _, err := store.SetLink(ctx, scope, "*", "neon", "db", values.Pair{}); err == nil {
		t.Fatal("SetLink() to the class-wide token succeeded, want it refused")
	}
	if _, err := store.SetLink(ctx, scope, "", "", "db", values.Pair{}); err == nil {
		t.Fatal("SetLink() with no publisher succeeded, want it refused")
	}
}

type counted struct {
	ports.RecordStore
	ports.Sealer
	mu     sync.Mutex
	reads  int
	lists  int
	opened int
}

func (c *counted) Read(ctx context.Context, name ports.RecordName) (ports.Record, error) {
	c.mu.Lock()
	c.reads++
	c.mu.Unlock()
	return c.RecordStore.Read(ctx, name)
}

func (c *counted) List(ctx context.Context, under ports.RecordName) ([]ports.Record, error) {
	c.mu.Lock()
	c.lists++
	c.mu.Unlock()
	return c.RecordStore.List(ctx, under)
}

func (c *counted) Open(ctx context.Context, at ports.Coordinate, sealed []byte) ([]byte, error) {
	c.mu.Lock()
	c.opened++
	c.mu.Unlock()
	return c.Sealer.Open(ctx, at, sealed)
}

func TestRevealReadsTheProjectOnceAndOpensEachCiphertextOnce(t *testing.T) {
	store, scope := fixture()
	ctx := context.Background()
	shared := values.Scope{Project: "platform", Class: ports.ClassProduction}

	if _, err := store.Set(ctx, shared, at("DATABASE_URL"), "postgres://shared", nil); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"A", "B", "C"} {
		if _, err := store.Set(ctx, scope, at(key), "held "+key, nil); err != nil {
			t.Fatal(err)
		}
	}
	target := values.Target{Project: "platform", Cell: values.Cell{Key: "DATABASE_URL"}}
	for _, key := range []string{"PRIMARY_URL", "REPLICA_URL"} {
		if _, err := store.SetReference(ctx, scope, at(key), target); err != nil {
			t.Fatal(err)
		}
	}

	watched := &counted{RecordStore: store.Records, Sealer: store.Sealer}
	store.Records, store.Sealer = watched, watched
	found, err := store.Reveal(ctx, scope, []values.Coordinate{
		at("A"), at("B"), at("C"), at("PRIMARY_URL"), at("REPLICA_URL"), at("NEVER_SET"),
	})
	if err != nil || len(found) != 5 {
		t.Fatalf("Reveal() = %d values, %v, want the five that hold one", len(found), err)
	}
	if watched.lists != 1 {
		t.Errorf("Reveal() listed the project's cells %d times, want one query for the whole batch", watched.lists)
	}
	if watched.reads != 1 {
		t.Errorf("Reveal() read %d cells one at a time, want only the borrowed cell in the other project", watched.reads)
	}
	if watched.opened != 4 {
		t.Errorf("Reveal() opened %d ciphertexts, want one per distinct cell behind the six asked for", watched.opened)
	}
}

func TestResolvingABatchOfLinksReadsEachEnvironmentOnce(t *testing.T) {
	store, scope := fixture()
	ctx := context.Background()

	publishing := make([]values.Publishing, 0, 3)
	for _, name := range []string{"db", "cache", "queue"} {
		publishing = append(publishing, values.Publishing{Name: name, Pair: values.Pair{Record: []byte("{}"), Value: []byte(`"` + name + `"`)}})
	}
	if _, err := store.SetLinks(ctx, scope, "", "OCEL", publishing); err != nil {
		t.Fatal(err)
	}

	watched := &counted{RecordStore: store.Records, Sealer: store.Sealer}
	store.Records, store.Sealer = watched, watched
	resolved, err := store.ResolveLinks(ctx, scope, "", []string{"db", "cache", "queue"})
	if err != nil || len(resolved) != 3 {
		t.Fatalf("ResolveLinks() = %+v, %v, want all three", resolved, err)
	}
	for i, name := range []string{"db", "cache", "queue"} {
		if string(resolved[i].Value) != `"`+name+`"` {
			t.Fatalf("ResolveLinks() answered %q for %s", resolved[i].Value, name)
		}
	}
	if watched.reads != 0 || watched.lists != 1 {
		t.Errorf("ResolveLinks() made %d point reads and %d queries, want one query serving the whole batch", watched.reads, watched.lists)
	}
}

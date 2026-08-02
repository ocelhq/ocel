package vars

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
)

// source is the cell a shared credential is set at: another project entirely,
// which is the whole point of referencing one.
func source() Coordinate {
	return Coordinate{Slug: "platform", Key: "STRIPE_API_KEY"}
}

// consumer is a cell in a different project and a different folder, so one
// resolution proves both axes a reference crosses.
func consumer() Coordinate {
	return Coordinate{Slug: "shop", Folder: "/checkout", Key: "STRIPE_API_KEY"}
}

// setReferenced sets a value at the source and points the consumer at it, the
// arrangement every test here starts from.
func setReferenced(t *testing.T, store *Store, plaintext string) {
	t.Helper()
	if _, err := store.Set(context.Background(), source(), plaintext, nil); err != nil {
		t.Fatalf("Set the source value err = %v", err)
	}
	if _, err := store.SetReference(context.Background(), consumer(), source(), nil); err != nil {
		t.Fatalf("SetReference err = %v", err)
	}
}

func TestReferenceReadsAsTheValueItPointsAt(t *testing.T) {
	store, ddb, _ := newTestStore(t)
	setReferenced(t, store, "sk_live_shared")

	got, err := store.Get(context.Background(), consumer(), true)
	if err != nil {
		t.Fatalf("Get err = %v", err)
	}
	if got.Plaintext != "sk_live_shared" {
		t.Errorf("resolved plaintext = %q, want the source's value", got.Plaintext)
	}
	if got.Target != source() {
		t.Errorf("Target = %v, want %v", got.Target, source())
	}

	// The value exists once. What the consumer's own partition holds is an
	// address, and nothing that would go stale if the source changed.
	for sk, item := range ddb.items[PartitionKey(consumer().Slug, store.Class)] {
		if len(binaryAttr(item, "ciphertext")) != 0 {
			t.Errorf("%s carries ciphertext of its own; a reference holds an address, not a copy", sk)
		}
	}
}

func TestReferenceResolvesToTheSourcesCurrentValue(t *testing.T) {
	store, _, _ := newTestStore(t)
	setReferenced(t, store, "first")

	if _, err := store.Set(context.Background(), source(), "rotated", nil); err != nil {
		t.Fatalf("Set err = %v", err)
	}

	got, err := store.Get(context.Background(), consumer(), true)
	if err != nil {
		t.Fatalf("Get err = %v", err)
	}
	if got.Plaintext != "rotated" {
		t.Errorf("plaintext after an edit at the source = %q, want %q with no write to the reference at all", got.Plaintext, "rotated")
	}
}

// The batch read is the one a deploy and a running function make, so a
// reference that resolves through Get and not through Reveal would work in a
// terminal and be absent in production.
func TestRevealResolvesAReferenceAcrossProjects(t *testing.T) {
	store, _, _ := newTestStore(t)
	setReferenced(t, store, "sk_live_shared")

	values, err := store.Reveal(context.Background(), consumer().Slug, []Coordinate{{Folder: consumer().Folder, Key: consumer().Key}})
	if err != nil {
		t.Fatalf("Reveal err = %v", err)
	}
	if len(values) != 1 {
		t.Fatalf("Reveal returned %d values, want 1", len(values))
	}
	if values[0].Plaintext != "sk_live_shared" {
		t.Errorf("revealed plaintext = %q, want the source's value", values[0].Plaintext)
	}
	if values[0].Coordinate != consumer() {
		t.Errorf("revealed coordinate = %v, want the cell that was asked for", values[0].Coordinate)
	}
}

func TestSetReferenceRejectsATargetThatIsItselfAReference(t *testing.T) {
	store, _, _ := newTestStore(t)
	setReferenced(t, store, "sk_live_shared")

	third := Coordinate{Slug: "admin", Key: "STRIPE_API_KEY"}
	_, err := store.SetReference(context.Background(), third, consumer(), nil)
	if !errors.Is(err, ErrWouldDeepen) {
		t.Fatalf("SetReference at a reference err = %v, want ErrWouldDeepen", err)
	}
	if _, err := store.Get(context.Background(), third, false); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get after the rejected write err = %v, want the write to have landed nowhere", err)
	}
}

// The other end of the same rule: a cell nothing points at may become a
// reference, and one that others read may not, or the chain they read through
// would silently be two hops long.
func TestSetReferenceRejectsACellOthersAlreadyReference(t *testing.T) {
	store, _, _ := newTestStore(t)
	setReferenced(t, store, "sk_live_shared")

	elsewhere := Coordinate{Slug: "platform", Key: "SHARED_SECRET"}
	if _, err := store.Set(context.Background(), elsewhere, "another value", nil); err != nil {
		t.Fatalf("Set err = %v", err)
	}

	_, err := store.SetReference(context.Background(), source(), elsewhere, nil)
	if !errors.Is(err, ErrWouldDeepen) {
		t.Fatalf("SetReference on a referenced cell err = %v, want ErrWouldDeepen", err)
	}
	if !strings.Contains(err.Error(), consumer().String()) {
		t.Errorf("refusal = %q, want it to name %s, which is what would have been left reading a reference", err, consumer())
	}
}

func TestSetReferenceRejectsACellPointedAtItself(t *testing.T) {
	store, _, _ := newTestStore(t)
	if _, err := store.Set(context.Background(), source(), "a value", nil); err != nil {
		t.Fatalf("Set err = %v", err)
	}

	if _, err := store.SetReference(context.Background(), source(), source(), nil); !errors.Is(err, ErrWouldDeepen) {
		t.Fatalf("self-referencing SetReference err = %v, want ErrWouldDeepen", err)
	}
}

// A named environment belongs to the project holding the reference. The target
// project has its own environments, sharing nothing but their names, so an
// override is not an address a reference can be pointed at.
func TestSetReferenceRejectsAnEnvironmentSpecificTarget(t *testing.T) {
	store, _, _ := newTestStore(t)
	target := source()
	target.Environment = "staging"
	if _, err := store.Set(context.Background(), target, "staging's own value", nil); err != nil {
		t.Fatalf("Set err = %v", err)
	}

	_, err := store.SetReference(context.Background(), consumer(), target, nil)
	if err == nil {
		t.Fatal("SetReference at an override err = nil, want a refusal")
	}
	if !strings.Contains(err.Error(), "class-wide") {
		t.Errorf("refusal = %q, want it to say a reference resolves against the class-wide value", err)
	}
}

func TestSetReferenceRejectsATargetHoldingNoValue(t *testing.T) {
	store, _, _ := newTestStore(t)

	_, err := store.SetReference(context.Background(), consumer(), source(), nil)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetReference at an unset cell err = %v, want ErrNotFound", err)
	}
	if !strings.Contains(err.Error(), source().String()) {
		t.Errorf("refusal = %q, want it to name the target that holds nothing", err)
	}
}

// Editing happens at the source, so there is exactly one place a value can be
// changed. A write to a reference is refused rather than quietly filed as a
// value of its own, which would leave two cells claiming to be the same one.
func TestSetRefusesToEditThroughAReference(t *testing.T) {
	store, _, _ := newTestStore(t)
	setReferenced(t, store, "sk_live_shared")

	_, err := store.Set(context.Background(), consumer(), "an edit made in the wrong place", nil)
	if !errors.Is(err, ErrIsReference) {
		t.Fatalf("Set through a reference err = %v, want ErrIsReference", err)
	}
	if !strings.Contains(err.Error(), source().String()) {
		t.Errorf("refusal = %q, want it to name where the value is edited", err)
	}

	got, err := store.Get(context.Background(), consumer(), true)
	if err != nil {
		t.Fatalf("Get err = %v", err)
	}
	if got.Plaintext != "sk_live_shared" {
		t.Errorf("value after the refused edit = %q, want it untouched", got.Plaintext)
	}
}

func TestReferencesAnswersWhatPointsAtAValueFromTheIndex(t *testing.T) {
	store, ddb, _ := newTestStore(t)
	setReferenced(t, store, "sk_live_shared")

	second := Coordinate{Slug: "admin", Key: "PAYMENTS_KEY"}
	if _, err := store.SetReference(context.Background(), second, source(), nil); err != nil {
		t.Fatalf("SetReference err = %v", err)
	}
	// A value of its own, in the same partition as one of the consumers, so the
	// answer is what points at the source rather than what sits near it.
	if _, err := store.Set(context.Background(), Coordinate{Slug: "admin", Key: "ADMIN_TOKEN"}, "unrelated", nil); err != nil {
		t.Fatalf("Set err = %v", err)
	}

	found, err := store.References(context.Background(), source())
	if err != nil {
		t.Fatalf("References err = %v", err)
	}
	want := []Coordinate{second, consumer()}
	if len(found) != len(want) {
		t.Fatalf("References = %v, want %v", found, want)
	}
	for i, c := range want {
		if found[i] != c {
			t.Errorf("References[%d] = %v, want %v", i, found[i], c)
		}
	}

	indexed := false
	for _, query := range ddb.queries {
		if aws.ToString(query.IndexName) == IndexName {
			indexed = true
		}
	}
	if !indexed {
		t.Error("References never queried the reverse index; the question it answers is a scan without one")
	}
}

// The index is sparse and holds current pointers only: a version row that once
// pointed somewhere, and a tombstone left where a reference was removed, are
// not things that reference a value now.
func TestReferencesCountsOnlyThePointersThatStillPoint(t *testing.T) {
	store, _, _ := newTestStore(t)
	setReferenced(t, store, "sk_live_shared")

	elsewhere := Coordinate{Slug: "platform", Key: "OTHER_KEY"}
	if _, err := store.Set(context.Background(), elsewhere, "another value", nil); err != nil {
		t.Fatalf("Set err = %v", err)
	}
	if _, err := store.SetReference(context.Background(), consumer(), elsewhere, nil); err != nil {
		t.Fatalf("re-pointing SetReference err = %v", err)
	}

	found, err := store.References(context.Background(), source())
	if err != nil {
		t.Fatalf("References err = %v", err)
	}
	if len(found) != 0 {
		t.Errorf("References after the pointer moved = %v, want none: only the version history still names the old target", found)
	}
}

// Removing a reference is removing an item. There is no unlink step, because
// there is nothing on the other side to unlink from: the source never recorded
// who reads it, the index simply stops answering.
func TestRemovingAReferenceNeedsNoUnlinkStep(t *testing.T) {
	store, _, _ := newTestStore(t)
	setReferenced(t, store, "sk_live_shared")

	deleted, err := store.Delete(context.Background(), consumer(), nil)
	if err != nil {
		t.Fatalf("Delete err = %v", err)
	}
	if !deleted {
		t.Error("Delete reported nothing to remove, want the reference removed")
	}

	if _, err := store.Get(context.Background(), consumer(), true); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get after removing the reference err = %v, want ErrNotFound", err)
	}
	found, err := store.References(context.Background(), source())
	if err != nil {
		t.Fatalf("References err = %v", err)
	}
	if len(found) != 0 {
		t.Errorf("References after the removal = %v, want none", found)
	}

	value, err := store.Get(context.Background(), source(), true)
	if err != nil {
		t.Fatalf("Get the source err = %v", err)
	}
	if value.Plaintext != "sk_live_shared" {
		t.Errorf("source value = %q, want removing a consumer to leave it alone", value.Plaintext)
	}
}

// Deleting at the source is allowed — the reverse lookup makes the blast radius
// visible rather than forbidding it — so what is left is a reference to nothing,
// and a read of one says so instead of reading as unset.
func TestReferenceToADeletedSourceFailsTheReadRatherThanReadingAsUnset(t *testing.T) {
	store, _, _ := newTestStore(t)
	setReferenced(t, store, "sk_live_shared")

	if _, err := store.Delete(context.Background(), source(), nil); err != nil {
		t.Fatalf("Delete err = %v", err)
	}

	_, err := store.Get(context.Background(), consumer(), true)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get through a reference to a deleted value err = %v, want ErrNotFound", err)
	}
	if !strings.Contains(err.Error(), source().String()) {
		t.Errorf("failure = %q, want it to name the cell that no longer holds a value", err)
	}
	if _, err := store.Reveal(context.Background(), consumer().Slug, []Coordinate{{Folder: consumer().Folder, Key: consumer().Key}}); err == nil {
		t.Error("Reveal err = nil, want the batch to fail rather than return a set of variables with one silently missing")
	}
}

func TestListShowsWhereAReferencePoints(t *testing.T) {
	store, _, _ := newTestStore(t)
	setReferenced(t, store, "sk_live_shared")

	found, err := store.List(context.Background(), consumer().Slug)
	if err != nil {
		t.Fatalf("List err = %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("List = %v, want the one reference", found)
	}
	if found[0].Target != source() {
		t.Errorf("Target = %v, want %v", found[0].Target, source())
	}
	if found[0].Size != 0 {
		t.Errorf("Size = %d, want 0: a reference holds an address, and the value's size is the source's", found[0].Size)
	}
}

// A reference occupies a version of the cell like anything else, so a cell that
// held a value and now holds a pointer keeps one version sequence rather than
// restarting on top of history it already wrote.
func TestAReferenceTakesTheNextVersionOfTheCellItReplaces(t *testing.T) {
	store, _, _ := newTestStore(t)
	if _, err := store.Set(context.Background(), source(), "the shared value", nil); err != nil {
		t.Fatalf("Set err = %v", err)
	}
	if _, err := store.Set(context.Background(), consumer(), "a copy someone pasted", nil); err != nil {
		t.Fatalf("Set err = %v", err)
	}

	written, err := store.SetReference(context.Background(), consumer(), source(), nil)
	if err != nil {
		t.Fatalf("SetReference err = %v", err)
	}
	if written.Version != 2 {
		t.Errorf("version = %d, want 2", written.Version)
	}

	versions, err := store.Versions(context.Background(), consumer())
	if err != nil {
		t.Fatalf("Versions err = %v", err)
	}
	if len(versions) != 2 {
		t.Fatalf("Versions = %v, want both the value and the reference that replaced it", versions)
	}
}

// What a deploy grants its functions is decided by this: the projects whose
// rows a reference will make them read. Its own project is not one of them —
// that partition is granted anyway — and a project referenced twice is named
// once.
func TestReferencedProjectsNamesEachOwnerOnce(t *testing.T) {
	store, _, _ := newTestStore(t)
	setReferenced(t, store, "sk_live_shared")

	second := Coordinate{Slug: consumer().Slug, Key: "PAYMENTS_KEY"}
	if _, err := store.SetReference(context.Background(), second, source(), nil); err != nil {
		t.Fatalf("SetReference err = %v", err)
	}
	own := Coordinate{Slug: consumer().Slug, Key: "LOCAL_KEY"}
	if _, err := store.Set(context.Background(), own, "a value of its own", nil); err != nil {
		t.Fatalf("Set err = %v", err)
	}
	within := Coordinate{Slug: consumer().Slug, Folder: "/admin", Key: "LOCAL_KEY"}
	if _, err := store.SetReference(context.Background(), within, own, nil); err != nil {
		t.Fatalf("SetReference err = %v", err)
	}

	owners, err := store.ReferencedProjects(context.Background(), consumer().Slug)
	if err != nil {
		t.Fatalf("ReferencedProjects err = %v", err)
	}
	if len(owners) != 1 || owners[0] != source().Slug {
		t.Errorf("ReferencedProjects = %v, want just %q", owners, source().Slug)
	}
}

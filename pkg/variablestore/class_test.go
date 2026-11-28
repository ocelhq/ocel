package variablestore_test

import (
	"context"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/variablestore"
)

func setClassed(t *testing.T, store variablestore.Store, scope variablestore.Scope, at variablestore.Coordinate, plaintext string, class variablestore.Class) variablestore.Metadata {
	t.Helper()
	written, err := store.Set(context.Background(), scope, []variablestore.ValueWrite{{At: at, Plaintext: plaintext, Class: class}})
	if err != nil {
		t.Fatal(err)
	}
	return written[0]
}

func classOf(t *testing.T, store variablestore.Store, scope variablestore.Scope, at variablestore.Coordinate) variablestore.Class {
	t.Helper()
	value, err := store.Get(context.Background(), scope, at, false)
	if err != nil {
		t.Fatal(err)
	}
	return value.Class
}

func TestAValueKeepsTheClassItWasWrittenAs(t *testing.T) {
	store, scope := fixture()

	for _, class := range []variablestore.Class{variablestore.ClassPlain, variablestore.ClassSensitive, variablestore.ClassSecret} {
		if written := setClassed(t, store, scope, at(class.String()), "value", class); written.Class != class {
			t.Errorf("Set() as %s answered %s", class, written.Class)
		}
		if got := classOf(t, store, scope, at(class.String())); got != class {
			t.Errorf("Get() of a value written as %s = %s", class, got)
		}
	}
}

func TestAValueWrittenWithNoClassReadsAsSensitiveUntilADeployClassifiesIt(t *testing.T) {
	store, scope := fixture()

	setClassed(t, store, scope, at("KEY"), "value", variablestore.ClassUnstated)
	if got := classOf(t, store, scope, at("KEY")); got != variablestore.ClassSensitive {
		t.Fatalf("a value written with no class reads as %s, want sensitive", got)
	}
	if _, err := store.Classify(context.Background(), scope, map[variablestore.Cell]variablestore.Class{{Key: "KEY"}: variablestore.ClassPlain}); err != nil {
		t.Fatal(err)
	}
	if got := classOf(t, store, scope, at("KEY")); got != variablestore.ClassPlain {
		t.Fatalf("a value written with no class, classified plain, reads as %s", got)
	}
}

func TestAnOverwriteWithNoClassKeepsTheClassTheValueHad(t *testing.T) {
	store, scope := fixture()

	setClassed(t, store, scope, at("KEY"), "one", variablestore.ClassSecret)
	setClassed(t, store, scope, at("KEY"), "two", variablestore.ClassUnstated)
	if got := classOf(t, store, scope, at("KEY")); got != variablestore.ClassSecret {
		t.Fatalf("a secret overwritten with no class reads as %s, want it still secret", got)
	}
}

func TestAnOverwriteWithAClassTakesThatClass(t *testing.T) {
	store, scope := fixture()

	setClassed(t, store, scope, at("KEY"), "one", variablestore.ClassSecret)
	setClassed(t, store, scope, at("KEY"), "two", variablestore.ClassPlain)
	if got := classOf(t, store, scope, at("KEY")); got != variablestore.ClassPlain {
		t.Fatalf("a secret overwritten as plain reads as %s, want plain: the secret it held is gone", got)
	}
}

func TestClassifyRaisesTheCellInEveryEnvironmentAndNeverLowersAStatedClass(t *testing.T) {
	store, scope := fixture()
	ctx := context.Background()
	staging := variablestore.Coordinate{Cell: variablestore.Cell{Key: "KEY"}, Environment: "staging"}

	setClassed(t, store, scope, at("KEY"), "tier-wide", variablestore.ClassPlain)
	setClassed(t, store, scope, staging, "staging", variablestore.ClassSensitive)
	setClassed(t, store, scope, at("OTHER"), "other", variablestore.ClassSecret)

	raised, err := store.Classify(ctx, scope, map[variablestore.Cell]variablestore.Class{
		{Key: "KEY"}:   variablestore.ClassSecret,
		{Key: "OTHER"}: variablestore.ClassPlain,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(raised) != 2 {
		t.Fatalf("Classify() raised %+v, want both environments of KEY and nothing else", raised)
	}
	for _, cell := range []variablestore.Coordinate{at("KEY"), staging, at("OTHER")} {
		if got := classOf(t, store, scope, cell); got != variablestore.ClassSecret {
			t.Errorf("%s reads as %s after Classify(), want secret", cell, got)
		}
	}
}

func TestClassifyLeavesAValueAtItsVersion(t *testing.T) {
	store, scope := fixture()
	ctx := context.Background()

	written := setClassed(t, store, scope, at("KEY"), "value", variablestore.ClassPlain)
	if _, err := store.Classify(ctx, scope, map[variablestore.Cell]variablestore.Class{{Key: "KEY"}: variablestore.ClassSecret}); err != nil {
		t.Fatal(err)
	}
	value, err := store.Get(ctx, scope, at("KEY"), true)
	if err != nil {
		t.Fatal(err)
	}
	if value.Version != written.Version || value.Plaintext != "value" {
		t.Fatalf("after Classify() the value is %+v, want version %d holding the same plaintext", value, written.Version)
	}
}

func TestAReferenceReadsAsTheStricterOfItsOwnClassAndItsSources(t *testing.T) {
	store, scope := fixture()
	ctx := context.Background()
	shared := variablestore.Scope{Project: "platform", Tier: environment.TierProduction}

	setClassed(t, store, shared, at("TOKEN"), "t0k3n", variablestore.ClassSecret)
	if _, err := store.SetReference(ctx, scope, at("TOKEN"), variablestore.Target{Project: "platform", Cell: variablestore.Cell{Key: "TOKEN"}}); err != nil {
		t.Fatal(err)
	}
	if got := classOf(t, store, scope, at("TOKEN")); got != variablestore.ClassSecret {
		t.Fatalf("a reference to a secret reads as %s through Get()", got)
	}
	listed, err := store.List(ctx, scope)
	if err != nil || len(listed) != 1 || listed[0].Class != variablestore.ClassSecret {
		t.Fatalf("List() = %+v, %v, want the reference read as secret", listed, err)
	}
	revealed, err := store.Reveal(ctx, scope, []variablestore.Coordinate{at("TOKEN")})
	if err != nil || len(revealed) != 1 || revealed[0].Class != variablestore.ClassSecret {
		t.Fatalf("Reveal() = %+v, %v, want the reference read as secret", revealed, err)
	}

	setClassed(t, store, shared, at("HOST"), "db.internal", variablestore.ClassPlain)
	if _, err := store.SetReference(ctx, scope, at("HOST"), variablestore.Target{Project: "platform", Cell: variablestore.Cell{Key: "HOST"}}); err != nil {
		t.Fatal(err)
	}
	if got := classOf(t, store, scope, at("HOST")); got != variablestore.ClassPlain {
		t.Fatalf("a reference to a plain value reads as %s, want plain", got)
	}
	if _, err := store.Classify(ctx, scope, map[variablestore.Cell]variablestore.Class{{Key: "HOST"}: variablestore.ClassSecret}); err != nil {
		t.Fatal(err)
	}
	if got := classOf(t, store, scope, at("HOST")); got != variablestore.ClassSecret {
		t.Fatalf("a reference its own project declares secret reads as %s, want secret", got)
	}
}

func TestAPublishedValueIsSecretOnlyWhenItWasPublishedAsOne(t *testing.T) {
	store, scope := fixture()
	ctx := context.Background()

	if _, err := store.SetPublished(ctx, scope, at("PASSWORD"), "pw", variablestore.Provenance{Publisher: infra, PublishedSecret: true}, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetPublished(ctx, scope, at("HOST"), "db.internal", variablestore.Provenance{Publisher: infra}, 0); err != nil {
		t.Fatal(err)
	}
	if got := classOf(t, store, scope, at("PASSWORD")); got != variablestore.ClassSecret {
		t.Errorf("a value published as a secret reads as %s", got)
	}
	if got := classOf(t, store, scope, at("HOST")); got != variablestore.ClassPlain {
		t.Errorf("a value published as no secret reads as %s, want plain", got)
	}
}

func TestAValueFromAnEnvSourceReadsAsSensitiveUntilADeployClassifiesIt(t *testing.T) {
	store, scope := fixture()
	ctx := context.Background()

	from := variablestore.Provenance{EnvSource: "vault", Version: "1", ReadAt: time.Unix(1, 0)}
	if _, err := store.SetFromEnvSource(ctx, scope, at("KEY"), "value", from, 0); err != nil {
		t.Fatal(err)
	}
	if got := classOf(t, store, scope, at("KEY")); got != variablestore.ClassSensitive {
		t.Fatalf("a value an env source wrote reads as %s, want sensitive", got)
	}
	if _, err := store.Classify(ctx, scope, map[variablestore.Cell]variablestore.Class{{Key: "KEY"}: variablestore.ClassPlain}); err != nil {
		t.Fatal(err)
	}
	if got := classOf(t, store, scope, at("KEY")); got != variablestore.ClassPlain {
		t.Fatalf("a value nobody stated a class for, classified plain, reads as %s", got)
	}
}

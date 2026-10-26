package variablestore_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ocelhq/ocel/pkg/variablestore"
)

const infra = "urn:pulumi:prod::infra::pulumi-nodejs:dynamic:Resource::ocel-variables-production"

func TestAPublishedValueRecordsItsPublisherAndWhetherItWasSecret(t *testing.T) {
	store, scope := fixture()
	ctx := context.Background()

	if _, err := store.SetPublished(ctx, scope, at("API_KEY"), "k3y", variablestore.Provenance{Publisher: infra, PublishedSecret: true}, 0); err != nil {
		t.Fatal(err)
	}

	value, err := store.Get(ctx, scope, at("API_KEY"), true)
	if err != nil {
		t.Fatal(err)
	}
	if value.Plaintext != "k3y" || value.Provenance.Publisher != infra || !value.Provenance.PublishedSecret {
		t.Fatalf("Get() = %+v, want k3y published secret by %s", value, infra)
	}
}

func TestAHandWriteOverAPublishedValueIsRefusedNamingItsPublisher(t *testing.T) {
	store, scope := fixture()
	ctx := context.Background()
	shared := variablestore.Scope{Project: "platform", Tier: scope.Tier}
	if _, err := store.Set(ctx, shared, at("API_KEY"), "elsewhere", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetPublished(ctx, scope, at("API_KEY"), "k3y", variablestore.Provenance{Publisher: infra}, 0); err != nil {
		t.Fatal(err)
	}

	for name, write := range map[string]func() error{
		"Set": func() error {
			_, err := store.Set(ctx, scope, at("API_KEY"), "by hand", nil)
			return err
		},
		"Delete": func() error {
			_, err := store.Delete(ctx, scope, at("API_KEY"), nil)
			return err
		},
		"SetReference": func() error {
			_, err := store.SetReference(ctx, scope, at("API_KEY"), variablestore.Target{Project: "platform", Cell: variablestore.Cell{Key: "API_KEY"}})
			return err
		},
		"SetPublished by another publisher": func() error {
			_, err := store.SetPublished(ctx, scope, at("API_KEY"), "other", variablestore.Provenance{Publisher: "urn:pulumi:prod::other::pulumi-nodejs:dynamic:Resource::ocel-variables-production"}, 1)
			return err
		},
	} {
		var published *variablestore.PublishedError
		if err := write(); !errors.As(err, &published) || published.Publisher != infra {
			t.Errorf("%s over a published value = %v, want a PublishedError naming %s", name, err, infra)
		}
	}

	value, err := store.Get(ctx, scope, at("API_KEY"), true)
	if err != nil || value.Plaintext != "k3y" {
		t.Fatalf("Get() = %+v, %v, want the published value untouched", value, err)
	}
}

func TestAPublisherTakesOverAHandSetValueAndRemovesWhatItPublished(t *testing.T) {
	store, scope := fixture()
	ctx := context.Background()
	from := variablestore.Provenance{Publisher: infra}
	if _, err := store.Set(ctx, scope, at("DATABASE_URL"), "by hand", nil); err != nil {
		t.Fatal(err)
	}

	if _, err := store.SetPublished(ctx, scope, at("DATABASE_URL"), "postgres://published", from, 1); err != nil {
		t.Fatalf("SetPublished() over a hand-set value = %v, want it written", err)
	}
	if removed, err := store.DeletePublished(ctx, scope, at("DATABASE_URL"), from, 2); err != nil || !removed {
		t.Fatalf("DeletePublished() = %v, %v, want it removed", removed, err)
	}
	if _, err := store.Set(ctx, scope, at("DATABASE_URL"), "by hand again", nil); err != nil {
		t.Fatalf("Set() after the publisher removed it = %v, want it written", err)
	}
}

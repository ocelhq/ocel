package component

import (
	"testing"

	"github.com/pulumi/pulumi-go-provider/infer"

	"github.com/ocelhq/ocel/pkg/naming"
)

func TestARandomTagIsMintedOnceWhenItsResourceIsCreated(t *testing.T) {
	t.Parallel()

	created, err := (&randomTag{}).Create(t.Context(), infer.CreateRequest[randomTagArgs]{Name: "ocel-app-shop-prod-uploads/tag"})
	if err != nil {
		t.Fatal(err)
	}
	if !naming.IsRandomTag(created.Output.Tag) || created.ID != created.Output.Tag {
		t.Errorf("Create = %+v, want a random tag as both its ID and its output", created)
	}
}

func TestAPlanMintsNoRandomTag(t *testing.T) {
	t.Parallel()

	planned, err := (&randomTag{}).Create(t.Context(), infer.CreateRequest[randomTagArgs]{Name: "ocel-app-shop-prod-uploads/tag", DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if planned.Output.Tag != "" {
		t.Errorf("a plan minted the tag %q, which the apply would not keep", planned.Output.Tag)
	}
}

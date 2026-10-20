package gcp

import (
	"context"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
)

func taggedDatabases(t *testing.T) (bootstrap.Engine, *googletest.Databases, *googletest.TierTags) {
	t.Helper()
	tags := googletest.NewTieredTags(t)
	databases := googletest.NewDatabases(t, tags)
	return bootstrap.Engine{Clients: tags.Tagged(googletest.Clients(databases.URL))}, databases, tags
}

func TestAProjectsDatabaseOfATierCarriesItsTiersTagFromItsCreation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b, databases, tags := taggedDatabases(t)
	id := b.Clients.ProjectDatabase(environment.TierPreview, "shop")

	for range 2 {
		if err := b.MakeTierDatabase(ctx, environment.TierPreview, id); err != nil {
			t.Fatal(err)
		}
	}
	if got := databases.InlineTags[id]; got["acme-prod/ocel-tier"] != "preview" {
		t.Errorf("the database was created with tags %v, want acme-prod/ocel-tier: preview", got)
	}
	if got := tags.Bound(googletest.DatabaseTagParent(id)); !slices.Equal(got, []string{tags.ValueOf(environment.TierPreview)}) {
		t.Errorf("the database carries %v, want the preview value once", got)
	}
	if tags.BindingsAsked != 1 {
		t.Errorf("Resource Manager was asked for %d bindings, want one, for the database found already made: the new one carried its tag from its creation", tags.BindingsAsked)
	}
}

func TestTheDatabaseBothTiersShareCarriesNoTierTag(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b, databases, tags := taggedDatabases(t)
	read := bootstrap.Survey{Tier: environment.TierProduction, Names: b.Clients.Names, Project: googletest.Project, Region: googletest.Region}

	if err := b.MakeDatabase(ctx, read, b.Clients.Database()); err != nil {
		t.Fatal(err)
	}
	if len(databases.InlineTags) != 0 || len(tags.BindingsMade) != 0 {
		t.Errorf("the shared database carries %v and %v, and either tier's value fences it from the other", databases.InlineTags, tags.BindingsMade)
	}
}

func TestRemovingAProjectsDatabaseRemovesItsInlineTagBeforeTheDatabase(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b, databases, _ := taggedDatabases(t)
	id := b.Clients.ProjectWritableDatabase(environment.TierProduction, "shop")
	if err := b.MakeTierDatabase(ctx, environment.TierProduction, id); err != nil {
		t.Fatal(err)
	}

	for range 2 {
		if err := b.TakeTierDatabase(ctx, environment.TierProduction, id); err != nil {
			t.Fatal(err)
		}
	}
	if len(databases.Deleted) != 1 || len(databases.DeletedTagged) != 0 {
		t.Errorf("deleted %v, of which %v still carried their tier tag, which Google then never lets go of", databases.Deleted, databases.DeletedTagged)
	}
}

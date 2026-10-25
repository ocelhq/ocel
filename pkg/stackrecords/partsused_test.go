package stackrecords_test

import (
	"context"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

func TestAPartIsUsedByTheRecordedProjectsWhoseLastDeployNeededIt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewKeyValues()
	tier := environment.TierProduction
	for _, project := range []string{"web", "docs", "gone"} {
		if err := stackrecords.RecordPartsUsed(ctx, store, tier, project, []string{"image-optimizer"}); err != nil {
			t.Fatalf("RecordPartsUsed(%s) = %v", project, err)
		}
	}
	if err := stackrecords.RecordPartsUsed(ctx, store, tier, "docs", nil); err != nil {
		t.Fatal(err)
	}
	for _, project := range []string{"web", "docs"} {
		if _, err := store.Write(ctx, records.Entry{Key: stackrecords.ProjectKey(tier, project), Value: []byte(`{}`)}); err != nil {
			t.Fatal(err)
		}
	}

	users, err := stackrecords.ListPartUsers(ctx, store, tier)
	if err != nil {
		t.Fatalf("ListPartUsers() = %v", err)
	}

	if got := users["image-optimizer"]; !slices.Equal(got, []string{"web"}) {
		t.Errorf("users of image-optimizer = %v, want web alone: docs no longer needs it and gone has no project record", got)
	}
}

func TestRecordingTheSamePartsAgainWritesNothing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewKeyValues()
	if err := stackrecords.RecordPartsUsed(ctx, store, environment.TierPreview, "web", []string{"cloudfront"}); err != nil {
		t.Fatal(err)
	}
	before, err := store.Read(ctx, stackrecords.PartsUsedKey(environment.TierPreview, "web"))
	if err != nil {
		t.Fatal(err)
	}

	if err := stackrecords.RecordPartsUsed(ctx, store, environment.TierPreview, "web", []string{"cloudfront"}); err != nil {
		t.Fatal(err)
	}

	after, err := store.Read(ctx, stackrecords.PartsUsedKey(environment.TierPreview, "web"))
	if err != nil {
		t.Fatal(err)
	}
	if after.Revision != before.Revision {
		t.Errorf("revision moved from %v to %v, want an unchanged record left alone", before.Revision, after.Revision)
	}
}

func TestRecordingTheSamePartsInAnotherOrderWritesNothing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewKeyValues()
	if err := stackrecords.RecordPartsUsed(ctx, store, environment.TierPreview, "web", []string{"network", "cloudfront"}); err != nil {
		t.Fatal(err)
	}
	before, err := store.Read(ctx, stackrecords.PartsUsedKey(environment.TierPreview, "web"))
	if err != nil {
		t.Fatal(err)
	}

	if err := stackrecords.RecordPartsUsed(ctx, store, environment.TierPreview, "web", []string{"cloudfront", "network"}); err != nil {
		t.Fatal(err)
	}

	after, err := store.Read(ctx, stackrecords.PartsUsedKey(environment.TierPreview, "web"))
	if err != nil {
		t.Fatal(err)
	}
	if after.Revision != before.Revision {
		t.Errorf("revision moved from %v to %v, want the record left alone: the parts a project uses are a set, whatever order a deploy lists them in", before.Revision, after.Revision)
	}
}

func TestAddingPartsKeepsThePartsAlreadyRecorded(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewKeyValues()
	tier := environment.TierProduction
	if err := stackrecords.RecordPartsUsed(ctx, store, tier, "shop", []string{"edge-bucket", "network"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Write(ctx, records.Entry{Key: stackrecords.ProjectKey(tier, "shop"), Value: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}

	if err := stackrecords.AddPartsUsed(ctx, store, tier, "shop", []string{"network", "topics"}); err != nil {
		t.Fatalf("AddPartsUsed() = %v", err)
	}

	users, err := stackrecords.ListPartUsers(ctx, store, tier)
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{"edge-bucket", "network", "topics"} {
		if !slices.Equal(users[part], []string{"shop"}) {
			t.Errorf("users of %s = %v, want shop: adding parts keeps what was recorded and adds the rest", part, users[part])
		}
	}
}

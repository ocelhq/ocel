package bootstrap

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	firestoreadmin "google.golang.org/api/firestore/v1"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
)

func TestAFirestoreOperationThatOutlastsAFastCallsWaitIsAwaitedUntilItEnds(t *testing.T) {
	if least := time.Duration(firestorePatience.Attempts) * firestorePatience.Ceiling / 2; least < 15*time.Minute {
		t.Errorf("a Firestore operation is given up on after as little as %s, want at least 15 minutes: an index build on a new database takes about 6", least)
	}
	patience := firestorePatience
	firestorePatience.Ceiling = time.Millisecond
	t.Cleanup(func() { firestorePatience = patience })
	databases := googletest.NewDatabases(t, nil)
	databases.OperationPolls = gcloud.WaitAttempts + 1
	b := Engine{Clients: googletest.Clients(databases.URL)}

	err := b.Awaited(context.Background(), "building an index on the runs of shop", &firestoreadmin.GoogleLongrunningOperation{
		Name: "projects/" + googletest.Project + "/databases/shop/operations/1",
	})
	if err != nil {
		t.Errorf("Awaited() = %v, want it to wait out an operation that ends on poll %d", err, databases.OperationPolls)
	}
}

func tieredDatabases(t *testing.T) (Engine, *googletest.Databases, *googletest.TierTags) {
	t.Helper()
	tags := googletest.NewTieredTags(t)
	databases := googletest.NewDatabases(t, tags)
	return Engine{Clients: tags.Tagged(googletest.Clients(databases.URL))}, databases, tags
}

func TestADatabaseWhoseCreateFailsToTagItIsTakenAsMadeWhenItExists(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b, databases, tags := tieredDatabases(t)
	id := b.Clients.OwnDatabase(environment.TierProduction, "shop")
	if err := b.MakeTierDatabase(ctx, environment.TierProduction, id); err != nil {
		t.Fatal(err)
	}
	databases.CreateUntaggable = true

	if err := b.MakeTierDatabase(ctx, environment.TierProduction, id); err != nil {
		t.Errorf("MakeTierDatabase() = %v, want the existing database taken as made: Firestore answers a create with tags of a database it holds with 500, not 409", err)
	}
	if got := tags.Bound(googletest.DatabaseTagParent(id)); !slices.Equal(got, []string{tags.ValueOf(environment.TierProduction)}) {
		t.Errorf("the database carries %v, want the production value once", got)
	}
}

func TestADatabaseWhoseCreateFailsAfterItsNamesakeWasDeletedIsRefusedSayingWhenTheNameIsFree(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b, databases, _ := tieredDatabases(t)
	id := b.Clients.OwnDatabase(environment.TierProduction, "shop")
	if err := b.MakeTierDatabase(ctx, environment.TierProduction, id); err != nil {
		t.Fatal(err)
	}
	if err := b.TakeTierDatabase(ctx, environment.TierProduction, id); err != nil {
		t.Fatal(err)
	}
	databases.CreateUntaggable = true

	err := b.MakeTierDatabase(ctx, environment.TierProduction, id)
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady || !strings.Contains(err.Error(), "5 minutes") {
		t.Errorf("MakeTierDatabase() = %v, want it refused as not ready, saying the name is free 5 minutes after the delete", err)
	}
}

func TestADatabaseFirestoreStillHoldsAsDeletedIsRefusedSayingWhenItsNameIsFree(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b, databases, _ := tieredDatabases(t)
	id := b.Clients.OwnDatabase(environment.TierProduction, "shop")
	databases.Held[id] = &firestoreadmin.GoogleFirestoreAdminV1Database{
		Name:       DatabasePath(b.Clients, id),
		LocationId: b.Clients.Region,
		DeleteTime: "2026-10-25T10:00:00Z",
	}
	databases.CreateUntaggable = true

	err := b.MakeTierDatabase(ctx, environment.TierProduction, id)
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady || !strings.Contains(err.Error(), "10:05") {
		t.Errorf("MakeTierDatabase() = %v, want it refused as not ready, saying the name is free at 10:05, 5 minutes after the delete", err)
	}
}

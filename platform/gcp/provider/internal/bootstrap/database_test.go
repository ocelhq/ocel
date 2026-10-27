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
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
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

func recordingInFirestore(t *testing.T) (Engine, Survey, *googletest.Firestore) {
	t.Helper()
	databases := googletest.NewDatabases(t, nil)
	served := googletest.NewFirestore(t, databases)
	clients := &gcloud.Clients{Names: googletest.Names, Region: googletest.Region, Endpoint: served.URL}
	clients.Records = ports.KeyValues{Clients: clients.Workload()}
	read := Survey{Tier: environment.TierProduction, Names: googletest.Names, Project: googletest.Project, Region: googletest.Region}
	return Engine{Clients: clients}, read, served
}

func claimStateBucket(ctx context.Context, b Engine) (string, error) {
	return b.Clients.ClaimTierBucket(ctx, environment.TierProduction, naming.StorageState, func() string {
		return googletest.Names.TierBucket(environment.TierProduction, naming.StorageState, googletest.BucketTag)
	})
}

func TestTheStateBucketIsRecordedInARecordsDatabaseFirestoreAnswersAsAbsentForAWhileAfterMakingIt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b, read, served := recordingInFirestore(t)
	database := databaseOf(read)
	if err := database.KindOps().Make(ctx, b, read, database); err != nil {
		t.Fatal(err)
	}
	served.AnswerAbsent(4)

	if _, err := claimStateBucket(ctx, b); err != nil {
		t.Fatalf("ClaimTierBucket() = %v, want the state bucket recorded once Firestore serves the database it just made", err)
	}
	if got := served.AnsweredAbsent(); got != 4 {
		t.Errorf("Firestore answered %d calls as for a database that does not exist, want all 4 it was set to answer so", got)
	}
	recorded, err := b.Clients.RecordedTierBuckets(ctx, environment.TierProduction)
	if err != nil || recorded[naming.StorageState] == "" {
		t.Errorf("RecordedTierBuckets() = %v, %v, want the state bucket on record", recorded, err)
	}
}

func TestRecordingTheStateBucketWithNoRecordsDatabaseRefusesAsNotBootstrapped(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b, read, _ := recordingInFirestore(t)

	_, err := claimStateBucket(ctx, b)

	want := `this project keeps no "` + read.Names.RecordsDatabase(read.Tier) + `" Firestore database, so there is nowhere to store an entry`
	if !errors.Is(err, ports.ErrNoRecordsDatabase) || !strings.Contains(err.Error(), want) {
		t.Errorf("ClaimTierBucket() = %v, want it refused saying %q", err, want)
	}
}

//go:build integration

package topics_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"cloud.google.com/go/firestore"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/runs"
	"github.com/ocelhq/ocel/pkg/runs/runsconformance"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
	"github.com/ocelhq/ocel/platform/gcp/provider/topics"
)

const (
	emulatorEndpointVariable = "OCEL_FLOCI_GCP_ENDPOINT"
	liveProjectVariable      = "OCEL_GCP_LIVE_PROJECT"
	liveRegionVariable       = "OCEL_GCP_LIVE_REGION"
	emulatedProject          = "floci-local"
	emulatedRegion           = "europe-west1"
)

func liveClients(t *testing.T) *ports.Clients {
	t.Helper()
	endpoint, project, region := os.Getenv(emulatorEndpointVariable), os.Getenv(liveProjectVariable), os.Getenv(liveRegionVariable)
	if endpoint == "" && project == "" {
		t.Fatalf("no floci-gcp emulator in the environment and %s names no project; run under `scripts/floci.sh --cloud gcp run <name> -- go test ./...`, or name a real project to run against", liveProjectVariable)
	}
	if project == "" {
		project = emulatedProject
	}
	if region == "" {
		region = emulatedRegion
	}
	return &ports.Clients{Namespace: "ocel", Project: project, Region: region, Endpoint: endpoint}
}

func TestLiveFirestoreStoreKeepsRunsAndRecordsAsEveryRunStoreMust(t *testing.T) {
	clients := liveClients(t)
	runsconformance.Store(t, func(t *testing.T) runs.Store {
		return topics.Store{Clients: clients, Scope: scopeOf(t)}
	})
}

func TestLiveATiersRunsAreKeptInItsOwnTaskDatabaseApartFromTheNamespaceRecords(t *testing.T) {
	clients := liveClients(t)
	scope := scopeOf(t)
	store := topics.Store{Clients: clients, Scope: scope}
	ctx := context.Background()
	now := time.Now()
	if _, err := store.CreateRun(ctx, runs.Run{Execution: "kept", Topic: "resize", Consumer: "resize", Status: provider.RunQueued, CreatedAt: now, DueAt: now}); err != nil {
		t.Fatal(err)
	}

	path := "taskstores/" + string(scope.Tier) + "~" + scope.Slug + "~" + scope.Environment + "/runs/kept"
	for database, want := range map[string]bool{
		ports.TaskDatabase(clients.Namespace, scope.Tier): true,
		clients.Database(): false,
	} {
		client, err := firestore.NewClientWithDatabase(ctx, clients.Project, database, ports.EmulatorGRPC(clients.Endpoint)...)
		if err != nil {
			t.Fatal(err)
		}
		snapshot, err := client.Doc(path).Get(ctx)
		if found := err == nil && snapshot.Exists(); found != want {
			t.Errorf("database %s holds the run: %v (%v), want %v", database, found, err, want)
		}
		_ = client.Close()
	}
}

func TestLiveAnUnfinishedRunIsPurgedAMonthAfterItWasDue(t *testing.T) {
	clients := liveClients(t)
	scope := scopeOf(t)
	store := topics.Store{Clients: clients, Scope: scope}
	ctx := context.Background()
	due := time.Now().Add(time.Hour).UTC().Truncate(time.Microsecond)
	if _, err := store.CreateRun(ctx, runs.Run{Execution: "pending", Topic: "resize", Consumer: "gone", Status: provider.RunDelayed, CreatedAt: time.Now(), DueAt: due}); err != nil {
		t.Fatal(err)
	}
	client, err := clients.TaskFirestore(scope.Tier)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := client.Doc("taskstores/" + string(scope.Tier) + "~" + scope.Slug + "~" + scope.Environment + "/runs/pending").Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	purgeAt, err := snapshot.DataAt("purgeAt")
	if at, ok := purgeAt.(time.Time); err != nil || !ok || !at.Equal(due.Add(topics.RunRetention)) {
		t.Errorf("the delayed run is purged at %v (%v), want %v so a run no consumer settles is not kept forever", purgeAt, err, due.Add(topics.RunRetention))
	}
}

func TestLiveAListingByManyTagsReadsABoundedPageAndCarriesOnFromItsCursor(t *testing.T) {
	store := topics.Store{Clients: liveClients(t), Scope: scopeOf(t)}
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Millisecond)
	tagged := func(execution string, created time.Time, tags ...string) runs.Run {
		return runs.Run{Execution: execution, Topic: "resize", Consumer: "resize", Status: provider.RunQueued, Payload: json.RawMessage(`{}`), Tags: tags, CreatedAt: created, DueAt: created}
	}
	if _, err := store.CreateRun(ctx, tagged("wanted", base.Add(-time.Hour), "eu", "big")); err != nil {
		t.Fatal(err)
	}
	const others = 1000
	work := make(chan int)
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			for i := range work {
				if _, err := store.CreateRun(ctx, tagged(fmt.Sprintf("other-%04d", i), base.Add(time.Duration(i)*time.Millisecond), "eu")); err != nil {
					t.Error(err)
				}
			}
		})
	}
	for i := range others {
		work <- i
	}
	close(work)
	wg.Wait()

	filter := runs.RunFilter{Topics: []string{"resize"}, Tags: []string{"eu", "big"}, Limit: 10}
	first, err := store.ListRuns(ctx, filter)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Runs) != 0 || first.NextCursor == "" {
		t.Fatalf("the first page holds %d runs with cursor %q, want none and a cursor: a listing reads at most a bounded number of runs", len(first.Runs), first.NextCursor)
	}
	filter.Cursor = first.NextCursor
	next, err := store.ListRuns(ctx, filter)
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Runs) != 1 || next.Runs[0].Execution != "wanted" || next.NextCursor != "" {
		t.Errorf("the page after the cursor holds %v with cursor %q, want only the run holding both tags and no cursor", next.Runs, next.NextCursor)
	}
}

func TestLiveARunOfAnotherTopicUnderTheSameExecutionIsNotFound(t *testing.T) {
	store := topics.Store{Clients: liveClients(t), Scope: scopeOf(t)}
	ctx := context.Background()
	now := time.Now()
	if _, err := store.CreateRun(ctx, runs.Run{Execution: "shared", Topic: "resize", Consumer: "resize", Status: provider.RunQueued, CreatedAt: now, DueAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadRun(ctx, "thumbnail", "shared"); !errors.Is(err, records.ErrNotFound) {
		t.Errorf("ReadRun of another topic's run = %v, want ErrNotFound", err)
	}
}

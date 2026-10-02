package topics_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/taskstoretest"
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
		t.Skipf("no floci-gcp emulator in the environment and %s names no project; run under `scripts/floci.sh --cloud gcp run <name> -- go test ./...`, or name a real project to run against", liveProjectVariable)
	}
	if project == "" {
		project = emulatedProject
	}
	if region == "" {
		region = emulatedRegion
	}
	return &ports.Clients{Namespace: "ocel", Project: project, Region: region, Endpoint: endpoint}
}

func scopeOf(t *testing.T) topics.Scope {
	t.Helper()
	sum := sha256.Sum256([]byte(t.Name() + time.Now().String()))
	return topics.Scope{Slug: "shop", Tier: environment.TierPreview, Environment: "t" + hex.EncodeToString(sum[:6])}
}

func TestLiveFirestoreStoreKeepsRunsAndRecordsAsEveryTaskStoreMust(t *testing.T) {
	clients := liveClients(t)
	taskstoretest.Run(t, func(t *testing.T) provider.TaskStore {
		return topics.Store{Clients: clients, Scope: scopeOf(t)}
	})
}

func TestLiveATiersRunsAreKeptInItsOwnTaskDatabaseApartFromTheNamespaceRecords(t *testing.T) {
	clients := liveClients(t)
	scope := scopeOf(t)
	store := topics.Store{Clients: clients, Scope: scope}
	ctx := context.Background()
	if _, err := store.WriteRun(ctx, provider.Run{Execution: "kept", Topic: "resize", Consumer: "resize", Status: provider.RunQueued, CreatedAt: time.Now()}); err != nil {
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

func TestATaskDatabaseIsNamedForItsNamespaceAndTier(t *testing.T) {
	t.Parallel()

	if got := ports.TaskDatabase("ocel", environment.TierProduction); got != "ocel-production-tasks" {
		t.Errorf("TaskDatabase(ocel, production) = %q, want ocel-production-tasks", got)
	}
}

func TestLiveAListingByManyTagsReadsABoundedPageAndCarriesOnFromItsCursor(t *testing.T) {
	store := topics.Store{Clients: liveClients(t), Scope: scopeOf(t)}
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Millisecond)
	wanted := provider.Run{Execution: "wanted", Topic: "resize", Consumer: "resize", Status: provider.RunQueued, Tags: []string{"eu", "big"}, CreatedAt: base.Add(-time.Hour)}
	if _, err := store.WriteRun(ctx, wanted); err != nil {
		t.Fatal(err)
	}
	const others = 1000
	work := make(chan int)
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			for i := range work {
				run := provider.Run{Execution: fmt.Sprintf("other-%04d", i), Topic: "resize", Consumer: "resize", Status: provider.RunQueued, Tags: []string{"eu"}, CreatedAt: base.Add(time.Duration(i) * time.Millisecond)}
				if _, err := store.WriteRun(ctx, run); err != nil {
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

	filter := provider.RunFilter{Topic: "resize", Tags: []string{"eu", "big"}, Limit: 10}
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

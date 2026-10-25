package bootstrap

import (
	"context"
	"testing"
	"time"

	firestoreadmin "google.golang.org/api/firestore/v1"

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

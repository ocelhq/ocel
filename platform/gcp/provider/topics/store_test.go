package topics_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"testing"
	"time"

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

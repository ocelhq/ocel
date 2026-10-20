package processenv_test

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/processenv"
)

func TestTheRuntimeEnvNamesAreTheOnesTheEdgeContractReads(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "platform", "edge", "contract", "fixtures", "runtime-env.json"))
	if err != nil {
		t.Fatal(err)
	}
	var want map[string]string
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{
		"storageCoordinate": processenv.StorageCoordinateEnvVar,
		"artifactsBucket":   processenv.BucketEnvVar(naming.StorageArtifacts),
		"edgeBucket":        processenv.BucketEnvVar(naming.StorageEdge),
		"cacheBucket":       processenv.BucketEnvVar(naming.StorageCache),
		"isr":               processenv.ISREnvVar,
		"routerKind":        processenv.RouterKindEnvVar,
		"routeTable":        processenv.RouteTableEnvVar,
		"buildId":           processenv.BuildIDEnvVar,
	}
	if !maps.Equal(got, want) {
		t.Errorf("the runtime env names are %v, want the %v the edge contract reads", got, want)
	}
}

func TestTheRuntimeLearnsTheStorageCoordinateAndEveryBucketButState(t *testing.T) {
	release := naming.Coordinate{Project: "shop", Env: "prod", App: "web", Release: naming.NewReleaseToken("b1", "f1")}
	buckets := naming.Buckets{naming.StorageState: "s", naming.StorageArtifacts: "a", naming.StorageEdge: "e", naming.StorageCache: "c"}
	want := map[string]string{
		"OCEL_STORAGE_COORDINATE": "shop/prod/web/" + release.Release.String(),
		"OCEL_ARTIFACTS_BUCKET":   "a",
		"OCEL_EDGE_BUCKET":        "e",
		"OCEL_CACHE_BUCKET":       "c",
	}
	if got := processenv.StorageEnv(release, buckets); !maps.Equal(got, want) {
		t.Errorf("StorageEnv() = %v, want %v", got, want)
	}
	if got := processenv.StorageEnv(release, naming.Buckets{naming.StorageEdge: "e"}); !maps.Equal(got, map[string]string{
		"OCEL_STORAGE_COORDINATE": "shop/prod/web/" + release.Release.String(),
		"OCEL_EDGE_BUCKET":        "e",
	}) {
		t.Errorf("StorageEnv() of an origin with only an edge bucket = %v, want the coordinate and that bucket", got)
	}
}

package processenv

import "github.com/ocelhq/ocel/pkg/naming"

const StorageCoordinateEnvVar = "OCEL_STORAGE_COORDINATE"

const ISREnvVar = "OCEL_ISR"

const CompileCacheEnvVar = "OCEL_COMPILE_CACHE"

const RouterKindEnvVar = "OCEL_ROUTER_KIND"

const RouteTableEnvVar = "OCEL_NEXT_ROUTE_TABLE"

const BuildIDEnvVar = "OCEL_BUILD_ID"

var bucketEnvVars = map[naming.StorageClass]string{
	naming.StorageArtifacts: "OCEL_ARTIFACTS_BUCKET",
	naming.StorageEdge:      "OCEL_EDGE_BUCKET",
	naming.StorageCache:     "OCEL_CACHE_BUCKET",
}

func BucketEnvVar(class naming.StorageClass) string {
	return bucketEnvVars[class]
}

func StorageEnv(storage naming.Coordinate, buckets naming.Buckets) map[string]string {
	env := map[string]string{StorageCoordinateEnvVar: storage.StorageCoordinate()}
	for class, variable := range bucketEnvVars {
		if bucket := buckets[class]; bucket != "" {
			env[variable] = bucket
		}
	}
	return env
}

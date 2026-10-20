package function

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/processenv"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/edges/cloudfront"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/bucket"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/variables"
	"github.com/ocelhq/ocel/platform/aws/provider/site"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
)

var storageVars = []string{
	processenv.StorageCoordinateEnvVar,
	processenv.BucketEnvVar(naming.StorageArtifacts),
	processenv.BucketEnvVar(naming.StorageEdge),
	processenv.BucketEnvVar(naming.StorageCache),
}

var everyBucket = naming.Buckets{
	naming.StorageState:     "state-1",
	naming.StorageArtifacts: "artifacts-1",
	naming.StorageEdge:      "edge-1",
	naming.StorageCache:     "cache-1",
}

func storedApp(t *testing.T, front edge.Edge) provider.StackSpec {
	t.Helper()
	return provider.StackSpec{
		Kind:     provider.StackApp,
		Recorded: true,
		Edge:     front,
		App:      &provider.AppSpec{App: "web", StorageCoordinate: routedCoordinate(t)},
	}
}

func storageEnvOf(env map[string]string) map[string]string {
	told := map[string]string{}
	for _, key := range storageVars {
		if value, set := env[key]; set {
			told[key] = value
		}
	}
	return told
}

func assertStorageEnv(t *testing.T, function string, env, want map[string]string) {
	t.Helper()
	got := storageEnvOf(env)
	for _, key := range storageVars {
		if got[key] != want[key] {
			t.Errorf("%s: %s = %q, want %q", function, key, got[key], want[key])
		}
	}
}

func TestAFunctionThatReadsNoStorageCarriesNoStorageVariables(t *testing.T) {
	t.Parallel()

	env := Env(AppEnv(storedApp(t, nil), variables.Bundle{}, bucket.Sessions{}, ""), Args{Handler: "index.handler"}, nil, nil)

	if told := storageEnvOf(env); len(told) != 0 {
		t.Errorf("a function with no ISR cache, compile cache or dispatch is told %v, spending its 4 KB environment on storage it never reads", told)
	}
}

func TestAnISRFunctionIsToldItsStorageCoordinateAndThePagesAndCacheBuckets(t *testing.T) {
	t.Parallel()

	cache := &site.ISR{Coord: routedCoordinate(t), Buckets: everyBucket}
	env := Env(AppEnv(storedApp(t, fakeEdgeOf(cloudfront.Kind)), variables.Bundle{}, bucket.Sessions{}, ""), Args{Handler: "index.handler"}, cache, nil)

	assertStorageEnv(t, "an ISR function", env, map[string]string{
		processenv.StorageCoordinateEnvVar:           "shop/prod/web/r3f8a1c90",
		processenv.BucketEnvVar(naming.StorageEdge):  "edge-1",
		processenv.BucketEnvVar(naming.StorageCache): "cache-1",
	})
}

func TestACompileCachingFunctionIsToldItsStorageCoordinateAndTheArtifactsBucket(t *testing.T) {
	t.Parallel()

	bytecode := &Bytecode{Bucket: "artifacts-1", StorageCoordinate: routedCoordinate(t)}
	env := Env(AppEnv(storedApp(t, fakeEdgeOf(cloudfront.Kind)), variables.Bundle{}, bucket.Sessions{}, ""), Args{Handler: "index.handler"}, nil, bytecode)

	assertStorageEnv(t, "a compile-caching function", env, map[string]string{
		processenv.StorageCoordinateEnvVar:               "shop/prod/web/r3f8a1c90",
		processenv.BucketEnvVar(naming.StorageArtifacts): "artifacts-1",
	})
}

func TestTheDispatchHostIsToldItsStorageCoordinateAndTheEdgeBucketItReadsAssetsFrom(t *testing.T) {
	t.Parallel()

	host := newDispatchHostFor(t, routedConfig(t, cloudfront.Kind))
	env := host.rootFunctionEnv(AppEnv(storedApp(t, fakeEdgeOf(cloudfront.Kind)), variables.Bundle{}, bucket.Sessions{}, ""))

	assertStorageEnv(t, "the dispatch host", env, map[string]string{
		processenv.StorageCoordinateEnvVar:          "shop/prod/web/r3f8a1c90",
		processenv.BucketEnvVar(naming.StorageEdge): "edge-bucket",
	})
}

func TestAFunctionBehindAnEdgeThatPurgesByCacheTagIsToldItsReleaseTokenAndNoStorage(t *testing.T) {
	t.Parallel()

	spec := storedApp(t, fakeEdgeOf(cloudflare.Kind))
	release := spec.App.StorageCoordinate.Release
	spec.Ref = provider.StackRef{Project: "shop", Name: naming.AppStack("prod", "web", release)}
	env := Env(AppEnv(spec, variables.Bundle{}, bucket.Sessions{}, ""), Args{Handler: "index.handler"}, nil, nil)

	if told := storageEnvOf(env); len(told) != 0 {
		t.Errorf("a function behind Cloudflare is told %v, want no storage variable: the release it tags responses with is its release token", told)
	}
	if got := env[edge.ReleaseTokenVar]; got != release.String() {
		t.Errorf("%s = %q, want the release token %q", edge.ReleaseTokenVar, got, release)
	}
}

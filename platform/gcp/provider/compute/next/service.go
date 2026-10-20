package next

import (
	"maps"
	"path"
	"strconv"
	"time"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/images"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/processenv"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/run"
)

const (
	MemoryMB           = 2048
	nextRequestTimeout = time.Minute
)

const (
	MemoryEnvVar            = "OCEL_FUNCTION_MEMORY_MB"
	RouterKindEnvVar        = "OCEL_ROUTER_KIND"
	RouteTableEnvVar        = "OCEL_NEXT_ROUTE_TABLE"
	slugEnvVar              = "OCEL_SLUG"
	appNameEnvVar           = "OCEL_APP"
	buildIDEnvVar           = "OCEL_BUILD_ID"
	isrTagNamespaceEnvVar   = "OCEL_ISR_TAG_NAMESPACE"
	storageEndpointEnvVar   = "OCEL_STORAGE_ENDPOINT"
	TagDatabaseEnvVar       = "OCEL_TAG_DATABASE"
	purgeSecretEnvVar       = "OCEL_PURGE_SECRET"
	firestoreEndpointEnvVar = "OCEL_FIRESTORE_ENDPOINT"
	imageEndpointEnvVar     = "OCEL_IMAGE_ENDPOINT"
)

var routeTableInImage = path.Join(images.FunctionImageRoot, edge.NextRouteTableFile)

func Serves(app *provider.AppSpec) bool {
	return app.Framework == buildoutput.FrameworkNext
}

func FillServingDefaults(s run.Serving) run.Serving {
	if s.Memory == 0 {
		s.Memory = MemoryMB
	}
	if s.Timeout == 0 {
		s.Timeout = nextRequestTimeout
	}
	return s
}

func RefuseGuardWithoutShieldingEdge(spec provider.StackSpec) error {
	app := spec.App
	if app.Guard == nil || spec.Edge == nil || spec.Edge.Kind() == edge.None || spec.Edge.Facts().ShieldsOrigin {
		return nil
	}
	return refusal.Refuse(refusal.CodeInvalid,
		"%s is served through %s, which keeps nobody off the Cloud Run url behind it, and on Cloud Run only the service's ingress "+
			"keeps clients from going around an edge: front it with an edge that shields its origin",
		app.App, spec.Edge.Kind())
}

type nextCache struct {
	buckets         naming.Buckets
	tagDatabase     string
	storageEndpoint string
	tagEndpoint     string
	purgeSecret     string
}

func SignsEdgePurges(spec provider.StackSpec) bool {
	return spec.App != nil && spec.App.ISR != nil &&
		spec.App.Compute == provider.ComputeServerless && run.FactsOf(spec.Edge).RunsCode
}

func NewEnv(spec provider.StackSpec, fn provider.FunctionSpec, s run.Serving, cache nextCache) map[string]string {
	app := spec.App
	env := naming.StorageEnv(app.Storage, cache.buckets)
	env[MemoryEnvVar] = strconv.Itoa(s.Memory)
	if app.Router != "" {
		env[RouterKindEnvVar] = string(app.Router)
	}
	if routing := app.Routing; routing != nil && resolveRouteID(fn) == routing.RootFunction {
		if run.FactsOf(spec.Edge).RunsCode {
			env[imageEndpointEnvVar] = "1"
		} else {
			env[edge.OriginDispatchVar] = "1"
			env[edge.OriginSignedVar] = "1"
		}
		env[RouteTableEnvVar] = routeTableInImage
		if cache.storageEndpoint != "" {
			env[storageEndpointEnvVar] = cache.storageEndpoint
		}
		if rules := app.Static.Variable(); rules != "" {
			env[edge.StaticRulesVar] = rules
		}
		env[slugEnvVar] = spec.Ref.Project
		env[appNameEnvVar] = app.App
		env[buildIDEnvVar] = app.BuildID
	}
	maps.Copy(env, newNextCacheEnv(app.ISR, cache))
	return env
}

func newNextCacheEnv(isr *provider.ISRSpec, cache nextCache) map[string]string {
	env := map[string]string{}
	if isr == nil {
		return env
	}
	env[processenv.ISREnvVar] = "1"
	env[isrTagNamespaceEnvVar] = isr.TagNamespace
	env[TagDatabaseEnvVar] = cache.tagDatabase
	if cache.purgeSecret != "" {
		env[purgeSecretEnvVar] = cache.purgeSecret
		if isr.PurgeURL != "" {
			env[edge.PurgeURLVar] = isr.PurgeURL
		}
	}
	if cache.storageEndpoint != "" {
		env[storageEndpointEnvVar] = cache.storageEndpoint
	}
	if cache.tagEndpoint != "" {
		env[firestoreEndpointEnvVar] = cache.tagEndpoint
	}
	return env
}

func NewContainerEnv(spec provider.StackSpec, memory int, cache nextCache) map[string]string {
	env := naming.StorageEnv(spec.App.Storage, cache.buckets)
	maps.Copy(env, newNextCacheEnv(spec.App.ISR, cache))
	env[MemoryEnvVar] = strconv.Itoa(memory)
	return env
}

func FillContainerDefaults(s run.Serving) run.Serving {
	if s.Memory == 0 {
		s.Memory = MemoryMB
	}
	return s
}

func NewCache(names names.Names, buckets names.TierBuckets, ref provider.StackRef, storageEndpoint, tagEndpoint, purgeSecret string) nextCache {
	return nextCache{
		purgeSecret:     purgeSecret,
		buckets:         naming.Buckets{naming.StorageEdge: buckets.Edge, naming.StorageCache: buckets.Cache},
		tagDatabase:     "projects/" + names.Project() + "/databases/" + names.ProjectWritableDatabase(ref.Tier, ref.Project),
		storageEndpoint: storageEndpoint,
		tagEndpoint:     tagEndpoint,
	}
}

func resolveRouteID(fn provider.FunctionSpec) string {
	if fn.Route != "" {
		return fn.Route
	}
	return fn.Name
}

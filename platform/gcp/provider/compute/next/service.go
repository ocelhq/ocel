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
	isrTagNamespaceEnvVar   = "OCEL_ISR_TAG_NAMESPACE"
	storageEndpointEnvVar   = "OCEL_STORAGE_ENDPOINT"
	TagDatabaseEnvVar       = "OCEL_TAG_DATABASE"
	firestoreEndpointEnvVar = "OCEL_FIRESTORE_ENDPOINT"
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
		spec.App.IsBuiltAsFunctions() && run.FactsOf(spec.Edge).RunsCode
}

func NewEnv(spec provider.StackSpec, fn provider.FunctionSpec, s run.Serving, cache nextCache) map[string]string {
	app := spec.App
	env := processenv.StorageEnv(app.StorageCoordinate, cache.buckets)
	env[MemoryEnvVar] = strconv.Itoa(s.Memory)
	if app.Router != "" {
		env[processenv.RouterKindEnvVar] = string(app.Router)
	}
	if routing := app.Routing; routing != nil && resolveRouteID(fn) == routing.RootFunction {
		if run.FactsOf(spec.Edge).RunsCode {
			env[edge.ImageEndpointVar] = "1"
		} else {
			maps.Copy(env, provider.NewOriginDispatchEnv())
		}
		maps.Copy(env, provider.NewRouteTableEnv(app, routeTableInImage))
		if cache.storageEndpoint != "" {
			env[storageEndpointEnvVar] = cache.storageEndpoint
		}
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
		env[edge.PurgeSecretVar] = cache.purgeSecret
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

func FillContainerDefaults(s run.Serving) run.Serving {
	if s.Memory == 0 {
		s.Memory = MemoryMB
	}
	return s
}

func NewCache(names names.Names, buckets naming.Buckets, database, storageEndpoint, tagEndpoint, purgeSecret string) nextCache {
	return nextCache{
		purgeSecret:     purgeSecret,
		buckets:         naming.Buckets{naming.StorageEdge: buckets[naming.StorageEdge], naming.StorageCache: buckets[naming.StorageCache]},
		tagDatabase:     "projects/" + names.Project() + "/databases/" + database,
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

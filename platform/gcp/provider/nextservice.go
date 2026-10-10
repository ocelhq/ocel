package gcp

import (
	"maps"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/images"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
)

const (
	nextMemoryMB       = 2048
	nextRequestTimeout = time.Minute
)

const (
	memoryEnvVar            = "OCEL_FUNCTION_MEMORY_MB"
	routerKindEnvVar        = "OCEL_ROUTER_KIND"
	routeTableEnvVar        = "OCEL_NEXT_ROUTE_TABLE"
	assetPrefixEnvVar       = "OCEL_ASSET_PREFIX"
	slugEnvVar              = "OCEL_SLUG"
	appNameEnvVar           = "OCEL_APP"
	buildIDEnvVar           = "OCEL_BUILD_ID"
	isrPrefixEnvVar         = "OCEL_ISR_PREFIX"
	isrTagNamespaceEnvVar   = "OCEL_ISR_TAG_NAMESPACE"
	isrBucketEnvVar         = "OCEL_ISR_BUCKET"
	isrObjectPrefixEnvVar   = "OCEL_ISR_OBJECT_PREFIX"
	storageEndpointEnvVar   = "OCEL_STORAGE_ENDPOINT"
	tagDatabaseEnvVar       = "OCEL_TAG_DATABASE"
	isrWriterURLEnvVar      = "OCEL_ISR_WRITER_URL"
	isrWriterSecretEnvVar   = "OCEL_ISR_WRITER_SECRET"
	firestoreEndpointEnvVar = "OCEL_FIRESTORE_ENDPOINT"
	staticDirEnvVar         = "OCEL_STATIC_DIR"
	imageEndpointEnvVar     = "OCEL_IMAGE_ENDPOINT"
	refreshURLEnvVar        = "OCEL_REFRESH_URL"
	refreshQueueEnvVar      = "OCEL_REFRESH_QUEUE"
	refreshAccountEnvVar    = "OCEL_REFRESH_ACCOUNT"
	refreshSecretEnvVar     = "OCEL_REFRESH_SECRET"
	refreshTargetEnvVar     = "OCEL_REFRESH_TARGET"
	tasksEndpointEnvVar     = "OCEL_TASKS_ENDPOINT"
	idTokenCertsURLEnvVar   = "OCEL_ID_TOKEN_CERTS_URL"
)

const refreshPath = "/_ocel/refresh"

var routeTableInImage = path.Join(images.FunctionImageRoot, edge.NextRouteTableFile)

func servesNext(app *provider.AppSpec) bool {
	return app.Framework == buildoutput.FrameworkNext
}

func refreshesByTask(framework string, compute provider.Compute, front edge.Facts, gated bool) bool {
	return framework == buildoutput.FrameworkNext && compute == provider.ComputeServerless && !front.RunsCode && !gated
}

func fillNextServingDefaults(s serving) serving {
	if s.memory == 0 {
		s.memory = nextMemoryMB
	}
	if s.timeout == 0 {
		s.timeout = nextRequestTimeout
	}
	return s
}

func refuseGuardWithoutShieldingEdge(spec provider.StackSpec) error {
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
	bucket          string
	tagDatabase     string
	storageEndpoint string
	tagEndpoint     string
	edgeStore       *edgeISRStore
}

type edgeISRStore struct {
	writer cloudflare.ISRWriter
	secret string
}

func keepsISRInEdgeStore(spec provider.StackSpec) bool {
	return spec.App != nil && servesNext(spec.App) && spec.App.ISR != nil &&
		spec.App.Compute == provider.ComputeServerless && factsOf(spec.Edge).RunsCode
}

type nextRefresh struct {
	url           string
	target        string
	queue         string
	account       string
	secret        string
	endpoint      string
	certsURL      string
	projectNumber int64
	region        string
}

func (r nextRefresh) addressedTo(service, tag string) *nextRefresh {
	r.url = refreshURLOf(service, r.projectNumber, r.region)
	if tag != "" {
		r.target = refreshTagURLOf(tag, service, r.projectNumber, r.region)
	}
	return &r
}

const maxRunAppLabelLength = 63

func refreshTagURLOf(tag, service string, projectNumber int64, region string) string {
	label := tag + "---" + service + "-" + strconv.FormatInt(projectNumber, 10)
	if len(label) > maxRunAppLabelLength {
		return ""
	}
	return "https://" + label + "." + region + ".run.app" + refreshPath
}

func refreshURLOf(service string, projectNumber int64, region string) string {
	return "https://" + service + "-" + strconv.FormatInt(projectNumber, 10) + "." + region + ".run.app" + refreshPath
}

func newNextEnv(spec provider.StackSpec, fn provider.FunctionSpec, s serving, cache nextCache, refresh *nextRefresh) map[string]string {
	app := spec.App
	env := map[string]string{memoryEnvVar: strconv.Itoa(s.memory)}
	maps.Copy(env, newCachingEdgeEnv(spec.Edge))
	if app.Router != "" {
		env[routerKindEnvVar] = string(app.Router)
	}
	if routing := app.Routing; routing != nil && resolveRouteID(fn) == routing.RootFunction {
		if factsOf(spec.Edge).RunsCode {
			env[imageEndpointEnvVar] = "1"
		} else {
			env[edge.OriginDispatchVar] = "1"
			env[edge.OriginSignedVar] = "1"
			if refresh != nil {
				env[refreshURLEnvVar] = refresh.url
				if refresh.target != "" {
					env[refreshTargetEnvVar] = refresh.target
				}
				env[refreshQueueEnvVar] = refresh.queue
				env[refreshAccountEnvVar] = refresh.account
				env[refreshSecretEnvVar] = refresh.secret
				if refresh.endpoint != "" {
					env[tasksEndpointEnvVar] = refresh.endpoint
				}
				if refresh.certsURL != "" {
					env[idTokenCertsURLEnvVar] = refresh.certsURL
				}
			}
		}
		env[routeTableEnvVar] = routeTableInImage
		env[staticDirEnvVar] = images.StaticRoot
		if rules := app.Static.Variable(); rules != "" {
			env[edge.StaticRulesVar] = rules
		}
		env[assetPrefixEnvVar] = app.AssetPrefix
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
	env[isrPrefixEnvVar] = isr.Prefix
	env[isrTagNamespaceEnvVar] = isr.TagNamespace
	env[isrBucketEnvVar] = cache.bucket
	env[isrObjectPrefixEnvVar] = strings.TrimSuffix(cacheObjectName(isr.Prefix+"/"), "/")
	if store := cache.edgeStore; store != nil {
		env[isrWriterURLEnvVar] = store.writer.Endpoint
		env[isrWriterSecretEnvVar] = store.secret
	} else {
		env[tagDatabaseEnvVar] = cache.tagDatabase
	}
	if cache.storageEndpoint != "" {
		env[storageEndpointEnvVar] = cache.storageEndpoint
	}
	if cache.tagEndpoint != "" && cache.edgeStore == nil {
		env[firestoreEndpointEnvVar] = cache.tagEndpoint
	}
	return env
}

func newNextContainerEnv(spec provider.StackSpec, memory int, cache nextCache) map[string]string {
	env := newNextCacheEnv(spec.App.ISR, cache)
	env[memoryEnvVar] = strconv.Itoa(memory)
	return env
}

func fillNextContainerDefaults(s serving) serving {
	if s.memory == 0 {
		s.memory = nextMemoryMB
	}
	return s
}

func newNextCache(names Names, tier environment.Tier, storageEndpoint, tagEndpoint string, edgeStore *edgeISRStore) nextCache {
	return nextCache{
		edgeStore:       edgeStore,
		bucket:          names.Bucket(tier),
		tagDatabase:     "projects/" + names.project + "/databases/" + names.TagDatabase(tier),
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

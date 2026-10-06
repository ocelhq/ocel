package gcp

import (
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/images"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

const (
	nextMemoryMB       = 2048
	nextRequestTimeout = time.Minute
)

const (
	memoryEnvVar            = "OCEL_FUNCTION_MEMORY_MB"
	routerKindEnvVar        = "OCEL_ROUTER_KIND"
	routingManifestEnvVar   = "OCEL_ROUTING_MANIFEST"
	assetPrefixEnvVar       = "OCEL_ASSET_PREFIX"
	slugEnvVar              = "OCEL_SLUG"
	appNameEnvVar           = "OCEL_APP"
	deploymentIDEnvVar      = "OCEL_DEPLOYMENT_ID"
	isrPrefixEnvVar         = "OCEL_ISR_PREFIX"
	isrTagNamespaceEnvVar   = "OCEL_ISR_TAG_NAMESPACE"
	isrBucketEnvVar         = "OCEL_ISR_BUCKET"
	isrObjectPrefixEnvVar   = "OCEL_ISR_OBJECT_PREFIX"
	storageEndpointEnvVar   = "OCEL_STORAGE_ENDPOINT"
	tagDatabaseEnvVar       = "OCEL_TAG_DATABASE"
	firestoreEndpointEnvVar = "OCEL_FIRESTORE_ENDPOINT"
	staticDirEnvVar         = "OCEL_STATIC_DIR"
	refreshURLEnvVar        = "OCEL_REFRESH_URL"
	refreshQueueEnvVar      = "OCEL_REFRESH_QUEUE"
	refreshAccountEnvVar    = "OCEL_REFRESH_ACCOUNT"
	refreshSecretEnvVar     = "OCEL_REFRESH_SECRET"
	tasksEndpointEnvVar     = "OCEL_TASKS_ENDPOINT"

	finishBeforeResponseEnvVar = "OCEL_FINISH_BEFORE_RESPONSE_MS"
)

const refreshPath = "/_ocel/refresh"

const finishBeforeResponseCap = 10 * time.Second

var routingManifestInImage = path.Join(images.FunctionImageRoot, edge.RoutingManifestFile)

func servesNext(app *provider.AppSpec) bool {
	return app.Framework == buildoutput.FrameworkNext
}

func refreshesByTask(spec provider.StackSpec) bool {
	app := spec.App
	return servesNext(app) && app.Compute == provider.ComputeServerless && app.Routing != nil && !factsOf(spec.Edge).RunsCode
}

func refreshesNextByTask(entry provider.AppEntry, front edge.Facts) bool {
	return entry.Manifest.GetFramework().GetName() == buildoutput.FrameworkNext && entry.Compute() == provider.ComputeServerless && !front.RunsCode
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
	bucket      string
	tagDatabase string
	endpoint    string
}

type nextRefresh struct {
	url      string
	queue    string
	account  string
	secret   string
	endpoint string
}

func refreshURLOf(service string, projectNumber int64, region string) string {
	return "https://" + service + "-" + strconv.FormatInt(projectNumber, 10) + "." + region + ".run.app" + refreshPath
}

func newNextEnv(spec provider.StackSpec, fn provider.FunctionSpec, s serving, cache nextCache, refresh *nextRefresh) map[string]string {
	app := spec.App
	env := map[string]string{memoryEnvVar: strconv.Itoa(s.memory)}
	if app.Router != "" {
		env[routerKindEnvVar] = string(app.Router)
	}
	if s.compute == provider.ComputeServerless {
		env[finishBeforeResponseEnvVar] = strconv.FormatInt(finishBeforeResponseCap.Milliseconds(), 10)
	}
	if routing := app.Routing; routing != nil && resolveRouteID(fn) == routing.Entry {
		if !factsOf(spec.Edge).RunsCode {
			env[edge.OriginDispatchVar] = "1"
			env[edge.OriginSignedVar] = "1"
			if refresh != nil {
				env[refreshURLEnvVar] = refresh.url
				env[refreshQueueEnvVar] = refresh.queue
				env[refreshAccountEnvVar] = refresh.account
				env[refreshSecretEnvVar] = refresh.secret
				if refresh.endpoint != "" {
					env[tasksEndpointEnvVar] = refresh.endpoint
				}
			}
		}
		env[routingManifestEnvVar] = routingManifestInImage
		env[staticDirEnvVar] = images.StaticRoot
		env[assetPrefixEnvVar] = app.AssetPrefix
		env[slugEnvVar] = spec.Ref.Project
		env[appNameEnvVar] = app.App
		env[deploymentIDEnvVar] = app.Deployment
	}
	if isr := app.ISR; isr != nil {
		env[isrPrefixEnvVar] = isr.Prefix
		env[isrTagNamespaceEnvVar] = isr.TagNamespace
		env[isrBucketEnvVar] = cache.bucket
		env[tagDatabaseEnvVar] = cache.tagDatabase
		env[isrObjectPrefixEnvVar] = strings.TrimSuffix(cacheObjectName(isr.Prefix+"/"), "/")
		if cache.endpoint != "" {
			env[storageEndpointEnvVar] = cache.endpoint
			env[firestoreEndpointEnvVar] = cache.endpoint
		}
	}
	return env
}

func resolveRouteID(fn provider.FunctionSpec) string {
	if fn.Route != "" {
		return fn.Route
	}
	return fn.Name
}

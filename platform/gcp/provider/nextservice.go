package gcp

import (
	"path"
	"strconv"
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
	memoryEnvVar          = "OCEL_FUNCTION_MEMORY_MB"
	routerKindEnvVar      = "OCEL_ROUTER_KIND"
	routingManifestEnvVar = "OCEL_ROUTING_MANIFEST"
	assetPrefixEnvVar     = "OCEL_ASSET_PREFIX"
	slugEnvVar            = "OCEL_SLUG"
	appNameEnvVar         = "OCEL_APP"
	deploymentIDEnvVar    = "OCEL_DEPLOYMENT_ID"
	isrPrefixEnvVar       = "OCEL_ISR_PREFIX"
	isrTagNamespaceEnvVar = "OCEL_ISR_TAG_NAMESPACE"
	staticDirEnvVar       = "OCEL_STATIC_DIR"

	finishBeforeResponseEnvVar = "OCEL_FINISH_BEFORE_RESPONSE_MS"
)

const finishBeforeResponseCap = 10 * time.Second

var routingManifestInImage = path.Join(images.FunctionImageRoot, edge.RoutingManifestFile)

func servesNext(app *provider.AppSpec) bool {
	return app.Framework == buildoutput.FrameworkNext
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

func newNextEnv(spec provider.StackSpec, fn provider.FunctionSpec, s serving) map[string]string {
	app := spec.App
	env := map[string]string{memoryEnvVar: strconv.Itoa(s.memory)}
	if app.Router != "" {
		env[routerKindEnvVar] = string(app.Router)
	}
	if s.compute == provider.ComputeServerless {
		env[finishBeforeResponseEnvVar] = strconv.FormatInt(finishBeforeResponseCap.Milliseconds(), 10)
	}
	if routing := app.Routing; routing != nil && routeOf(fn) == routing.Entry {
		if !factsOf(spec.Edge).RunsCode {
			env[edge.OriginDispatchVar] = "1"
			env[edge.OriginSignedVar] = "1"
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
	}
	return env
}

func routeOf(fn provider.FunctionSpec) string {
	if fn.Route != "" {
		return fn.Route
	}
	return fn.Name
}

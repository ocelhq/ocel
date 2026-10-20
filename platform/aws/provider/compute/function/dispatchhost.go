package function

import (
	"encoding/json"
	"fmt"
	"maps"
	"strings"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
)

const (
	routerKindEnv        = "OCEL_ROUTER_KIND"
	routeTableEnv        = "OCEL_NEXT_ROUTE_TABLE"
	functionURLsEnv      = "OCEL_FUNCTION_URLS"
	slugEnv              = "OCEL_SLUG"
	appNameEnv           = "OCEL_APP"
	buildIDEnv           = "OCEL_BUILD_ID"
	routeTableInTask     = "/var/task/" + edge.NextRouteTableFile
	ocelRouteTableInTask = "/var/task/" + edge.OcelRouteTableFile

	functionURLBudgetBytes = 80
)

type DispatchHost struct {
	RootFunction      string
	EdgeBucket        string
	Storage           naming.Coordinate
	ImageOptimizerURL string
	Env               map[string]string
}

func (h *DispatchHost) hosts(fn AppFunction) bool {
	return h != nil && fn.route() == h.RootFunction
}

func (h *DispatchHost) rootFunctionEnv(base map[string]string) map[string]string {
	if h == nil {
		return base
	}
	env := make(map[string]string, len(base)+len(h.Env))
	maps.Copy(env, base)
	maps.Copy(env, h.Env)
	return env
}

func (h *DispatchHost) plannedRootFunctionEnv(base map[string]string, functions []AppFunction) map[string]string {
	env := h.rootFunctionEnv(base)
	size := len("{}")
	for _, fn := range functions {
		if h.hosts(fn) {
			continue
		}
		size += len(fn.route()) + functionURLBudgetBytes
	}
	env[functionURLsEnv] = strings.Repeat("x", size)
	return env
}

func siblingFunctionURLs(urls pulumi.StringMap) pulumi.StringOutput {
	return urls.ToStringMapOutput().ApplyT(func(resolved map[string]string) (string, error) {
		encoded, err := json.Marshal(resolved)
		if err != nil {
			return "", fmt.Errorf("render the sibling Function URLs the root function routes to: %w", err)
		}
		return string(encoded), nil
	}).(pulumi.StringOutput)
}

func NewDispatchHost(app *provider.AppSpec, slug, edgeBucket, imageOptimizerURL string) *DispatchHost {
	routing := app.Routing
	if routing == nil {
		return nil
	}
	host := &DispatchHost{
		RootFunction:      routing.RootFunction,
		EdgeBucket:        edgeBucket,
		Storage:           app.Storage,
		ImageOptimizerURL: imageOptimizerURL,
		Env: map[string]string{
			routeTableEnv: routeTableInTask,
			slugEnv:       slug,
			appNameEnv:    app.App,
			buildIDEnv:    app.BuildID,
		},
	}
	maps.Copy(host.Env, naming.StorageEnv(app.Storage, naming.Buckets{naming.StorageEdge: edgeBucket}))
	if imageOptimizerURL != "" {
		host.Env[edge.ImageOptimizerURLVar] = imageOptimizerURL
	}
	if rules := app.Static.Variable(); rules != "" {
		host.Env[edge.StaticRulesVar] = rules
	}
	return host
}

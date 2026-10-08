package deploy

import (
	"encoding/json"
	"fmt"
	"maps"
	"strings"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/edge"
)

const (
	routerKindEnv    = "OCEL_ROUTER_KIND"
	routeTableEnv    = "OCEL_NEXT_ROUTE_TABLE"
	functionURLsEnv  = "OCEL_FUNCTION_URLS"
	assetBucketEnv   = "OCEL_ASSET_BUCKET"
	assetPrefixEnv   = "OCEL_ASSET_PREFIX"
	slugEnv          = "OCEL_SLUG"
	appNameEnv       = "OCEL_APP"
	buildIDEnv       = "OCEL_BUILD_ID"
	routeTableInTask = "/var/task/" + edge.NextRouteTableFile

	functionURLBudgetBytes = 80
)

type dispatchHost struct {
	RootFunction      string
	AssetBucket       string
	AssetPrefix       string
	ImageOptimizerURL string
	Env               map[string]string
}

func (h *dispatchHost) hosts(fn appFunction) bool {
	return h != nil && fn.route() == h.RootFunction
}

func (h *dispatchHost) rootFunctionEnv(base map[string]string) map[string]string {
	if h == nil {
		return base
	}
	env := make(map[string]string, len(base)+len(h.Env))
	maps.Copy(env, base)
	maps.Copy(env, h.Env)
	return env
}

func (h *dispatchHost) plannedRootFunctionEnv(base map[string]string, functions []appFunction) map[string]string {
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

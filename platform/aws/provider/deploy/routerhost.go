package deploy

import (
	"encoding/json"
	"fmt"
	"maps"
	"strings"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	edgeKindEnv           = "OCEL_EDGE_KIND"
	routingManifestEnv    = "OCEL_ROUTING_MANIFEST"
	functionURLsEnv       = "OCEL_FUNCTION_URLS"
	assetBucketEnv        = "OCEL_ASSET_BUCKET"
	assetPrefixEnv        = "OCEL_ASSET_PREFIX"
	slugEnv               = "OCEL_SLUG"
	appNameEnv            = "OCEL_APP"
	deploymentIDEnv       = "OCEL_DEPLOYMENT_ID"
	routingManifestInTask = "/var/task/" + edge.RoutingManifestFile

	functionURLBudgetBytes = 80
)

type routerHost struct {
	Entry             string
	AssetBucket       string
	AssetPrefix       string
	ImageOptimizerURL string
	Env               map[string]string
}

func (h *routerHost) hosts(fn appFunction) bool {
	return h != nil && fn.route() == h.Entry
}

func (h *routerHost) entryEnv(base map[string]string) map[string]string {
	if h == nil {
		return base
	}
	env := make(map[string]string, len(base)+len(h.Env))
	maps.Copy(env, base)
	maps.Copy(env, h.Env)
	return env
}

func (h *routerHost) plannedEntryEnv(base map[string]string, functions []appFunction) map[string]string {
	env := h.entryEnv(base)
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
			return "", fmt.Errorf("render the sibling Function URLs the entry function routes to: %w", err)
		}
		return string(encoded), nil
	}).(pulumi.StringOutput)
}

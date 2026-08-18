package deploy

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/naming"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
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
	Manifest          []byte
	AssetBucket       string
	AssetPrefix       string
	ImageOptimizerURL string
	Env               map[string]string
}

func (h *routerHost) hosts(fn *deploymentsv1.ManifestFunction) bool {
	return h != nil && routeID(fn) == h.Entry
}

func (h *routerHost) overlay() map[string][]byte {
	return map[string][]byte{edge.RoutingManifestFile: h.Manifest}
}

func withOverlay(base, extra map[string][]byte) map[string][]byte {
	merged := make(map[string][]byte, len(base)+len(extra))
	maps.Copy(merged, base)
	maps.Copy(merged, extra)
	return merged
}

func resolveRouterHost(cfg Config, app *deploymentsv1.ManifestApp, coord naming.Coordinate, deploymentID string) (*routerHost, error) {
	if cfg.Edge == nil || cfg.Edge.Kind() == edge.KindCloudflare {
		return nil, nil
	}
	name := app.GetName()
	desc, ok, err := readServeDescriptor(cfg.ArtifactRoot, name)
	if err != nil {
		return nil, err
	}
	if !ok || !desc.EdgeRouting {
		return nil, nil
	}
	raw, err := os.ReadFile(filepath.Join(appArtifactRoot(cfg.ArtifactRoot, name), edge.RoutingManifestFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("app %s routes at its origin but its build wrote no %s; rebuild the app", name, edge.RoutingManifestFile)
	}
	if err != nil {
		return nil, fmt.Errorf("read the routing manifest %s hosts at its origin: %w", name, err)
	}
	if desc.Entry == "" {
		return nil, fmt.Errorf("app %s routes at its origin but its build names no entry route; rebuild the app", name)
	}

	host := &routerHost{
		Entry:             desc.Entry,
		Manifest:          raw,
		AssetBucket:       cfg.AssetBucket,
		AssetPrefix:       appAssetPrefix(coord),
		ImageOptimizerURL: cfg.ImageOptimizerURL,
		Env: map[string]string{
			routingManifestEnv:         routingManifestInTask,
			assetPrefixEnv:             appAssetPrefix(coord),
			slugEnv:                    cfg.Slug,
			appNameEnv:                 name,
			deploymentIDEnv:            deploymentID,
			edge.OriginBodyLimitVar:    strconv.Itoa(lambdaOriginBodyLimitBytes),
			edge.OriginBodyEncodingVar: edge.OriginBodyEncodingBase64,
		},
	}
	if cfg.AssetBucket != "" {
		host.Env[assetBucketEnv] = cfg.AssetBucket
	}
	if cfg.ImageOptimizerURL != "" {
		host.Env[edge.ImageOptimizerURLVar] = cfg.ImageOptimizerURL
	}
	return host, nil
}

func (h *routerHost) entryEnv(base map[string]string) map[string]string {
	env := make(map[string]string, len(base)+len(h.Env))
	maps.Copy(env, base)
	maps.Copy(env, h.Env)
	return env
}

func (h *routerHost) plannedEntryEnv(base map[string]string, functions []*deploymentsv1.ManifestFunction) map[string]string {
	env := h.entryEnv(base)
	size := len("{}")
	for _, fn := range functions {
		if h.hosts(fn) {
			continue
		}
		size += len(routeID(fn)) + functionURLBudgetBytes
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

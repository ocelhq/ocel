package alb

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/transform"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/albresources"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/surface"
)

var ownedFields = map[string][]string{
	albresources.SurfaceBackendService: {
		"backends", "cdnPolicy.bypassCacheOnRequestHeaders", "cdnPolicy.cacheKeyPolicy", "cdnPolicy.cacheMode",
		"deletionPolicy", "enableCdn", "externalManagedMigrationState", "externalManagedMigrationTestingPercentage",
		"loadBalancingScheme", "name", "params", "project", "protocol",
	},
	albresources.SurfaceNEG: {
		"appEngine", "cloudFunction", "cloudRun", "deletionPolicy", "name", "network", "networkEndpointType",
		"project", "pscData", "pscTargetService", "region", "serverlessDeployment", "subnetwork",
	},
	albresources.SurfaceCertificateMapEntry: {"certificates", "deletionPolicy", "hostname", "labels", "map", "matcher", "name", "project"},
	albresources.SurfaceAddress: {
		"address", "addressType", "deletionPolicy", "ipVersion", "labels", "name", "network", "prefixLength",
		"project", "purpose",
	},
	albresources.SurfaceNotFoundBackend: {
		"backends", "cdnPolicy", "deletionPolicy", "enableCdn", "externalManagedMigrationState",
		"externalManagedMigrationTestingPercentage", "loadBalancingScheme", "name", "params", "project", "protocol",
	},
	albresources.SurfaceAssetsBackend: {
		"bucketName", "cdnPolicy.cacheMode", "cdnPolicy.negativeCaching", "deletionPolicy", "enableCdn",
		"loadBalancingScheme", "name", "params", "project",
	},
	albresources.SurfaceCertificateMap: {"deletionPolicy", "labels", "name", "project"},
	albresources.SurfaceURLMap: {
		"defaultRouteAction", "defaultService", "defaultUrlRedirect", "deletionPolicy", "headerAction", "hostRules",
		"name", "pathMatchers", "project", "tests",
	},
	albresources.SurfaceHTTPSProxy: {
		"certificateManagerCertificates", "certificateMap", "deletionPolicy", "name", "project", "proxyBind",
		"sslCertificates", "urlMap",
	},
	albresources.SurfaceForwardingRule: {
		"allowPscGlobalAccess", "deletionPolicy", "externalManagedBackendBucketMigrationState",
		"externalManagedBackendBucketMigrationTestingPercentage", "ipAddress", "ipProtocol", "ipVersion", "labels",
		"loadBalancingScheme", "metadataFilters", "name", "network", "networkTier", "noAutomateDnsZone", "portRange",
		"project", "serviceDirectoryRegistrations", "sourceIpRanges", "subnetwork", "target",
	},
}

func EvaluateFront(ctx context.Context, pass transform.Pass, tier environment.Tier) (transform.Result, error) {
	if pass == nil {
		return transform.Result{}, nil
	}
	req := surface.Request(tier, "", []transform.Resource{{Type: albresources.TransformKindLoadBalancer, Name: string(tier)}})
	results, err := evaluateOne(ctx, pass, req)
	if err != nil || results == nil {
		return transform.Result{}, err
	}
	if err := transform.WalkOutputs(req, results, func(ref transform.OutputRef, at transform.OutputSite, _ any) (any, error) {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"a transform fills the %s tier's load balancer's %s.%s from %s, and the load balancer belongs to every project of the tier, so it reads no project's records",
			tier, at.Surface, at.Field, ref)
	}); err != nil {
		return transform.Result{}, err
	}
	named := "the " + string(tier) + " tier's load balancer"
	if err := refuseUnpatchable(named, results[0], albresources.LoadBalancerSurfaces); err != nil {
		return transform.Result{}, err
	}
	if _, _, err := albresources.TakeHeaders(results[0].Patches[albresources.SurfaceAssetsBackend], named+"'s "+albresources.SurfaceAssetsBackend); err != nil {
		return transform.Result{}, err
	}
	return results[0], nil
}

func EvaluateApp(ctx context.Context, pass transform.Pass, spec provider.StackSpec) (transform.Result, error) {
	if pass == nil || spec.App == nil || spec.Edge == nil || spec.Edge.Kind() != albresources.Kind {
		return transform.Result{}, nil
	}
	app := spec.App.App
	req := surface.Request(spec.Ref.Tier, "", []transform.Resource{{Type: albresources.TransformKindEdge, Name: app, App: app}})
	results, err := evaluateOne(ctx, pass, req)
	if err != nil || results == nil {
		return transform.Result{}, err
	}
	if err := refuseUnpatchable(albresources.TransformKindEdge+" "+app, results[0], albresources.EdgeSurfaces); err != nil {
		return transform.Result{}, err
	}
	if err := transform.ResolveOutputs(ctx, spec, req, results); err != nil {
		return transform.Result{}, err
	}
	if _, _, err := albresources.TakeHeaders(results[0].Patches[albresources.SurfaceBackendService], albresources.TransformKindEdge+" "+app+"'s "+albresources.SurfaceBackendService); err != nil {
		return transform.Result{}, err
	}
	return results[0], nil
}

func evaluateOne(ctx context.Context, pass transform.Pass, req transform.Request) ([]transform.Result, error) {
	results, err := pass.Evaluate(ctx, req)
	if err != nil || results == nil {
		return nil, err
	}
	if len(results) != 1 {
		return nil, fmt.Errorf("transforms returned %d results for %s %s", len(results), req.Resources[0].Type, req.Resources[0].Name)
	}
	return results, nil
}

func refuseUnpatchable(named string, result transform.Result, constructed []string) error {
	for _, key := range slices.Sorted(maps.Keys(result.Patches)) {
		if !slices.Contains(constructed, key) {
			return refusal.Refuse(refusal.CodeInvalid,
				"a transform patches %s's %s, and the %s edge constructs no such resource for it: patch %s",
				named, key, albresources.Kind, strings.Join(constructed, ", "))
		}
	}
	for _, key := range slices.Sorted(maps.Keys(result.Patches)) {
		if err := transform.RefuseOwned(named+"'s "+key, result.Patches[key], ownedFields[key]); err != nil {
			return err
		}
	}
	return surface.RefuseInvalidLabels(named, result.Tags)
}

type recordedPatches struct {
	Patches transform.Patches `json:"patches,omitempty"`
	Tags    map[string]string `json:"tags,omitempty"`
}

func patchesOf(result transform.Result) recordedPatches {
	return recordedPatches{Patches: result.Patches, Tags: result.Tags}
}

func (r recordedPatches) result() transform.Result {
	return transform.Result{Patches: r.Patches, Tags: r.Tags}
}

func (r recordedPatches) isEmpty() bool {
	for _, patch := range r.Patches {
		if len(patch) > 0 {
			return false
		}
	}
	return len(r.Tags) == 0
}

func (r recordedPatches) equal(other recordedPatches) bool {
	mine, err := json.Marshal(r)
	if err != nil {
		return false
	}
	theirs, err := json.Marshal(other)
	return err == nil && string(mine) == string(theirs)
}

type AppPatches struct {
	mu      sync.Mutex
	results map[appKey]transform.Result
}

type appKey struct {
	tier environment.Tier
	slug string
	app  string
}

func (a *AppPatches) Set(tier environment.Tier, slug, app string, result transform.Result) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.results == nil {
		a.results = map[appKey]transform.Result{}
	}
	a.results[appKey{tier: tier, slug: slug, app: app}] = result
}

func (a *AppPatches) Read(_ context.Context, tier environment.Tier, slug string) (map[string]transform.Result, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	read := map[string]transform.Result{}
	for key, result := range a.results {
		if key.tier == tier && key.slug == slug {
			read[key.app] = result
		}
	}
	return read, nil
}

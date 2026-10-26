package alb

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	ocelpulumi "github.com/ocelhq/ocel/pkg/provider/pulumi"
	"github.com/ocelhq/ocel/pkg/provider/transform"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/surface"
)

const (
	TransformKindEdge         = "edge"
	TransformKindLoadBalancer = "loadBalancer"

	surfaceBackendService      = "backendService"
	surfaceNEG                 = "neg"
	surfaceCertificateMapEntry = "certificateMapEntry"

	surfaceAddress         = "address"
	surfaceNotFoundBackend = "notFoundBackend"
	surfaceAssetsBackend   = "assetsBackend"
	surfaceCertificateMap  = "certificateMap"
	surfaceURLMap          = "urlMap"
	surfaceHTTPSProxy      = "httpsProxy"
	surfaceForwardingRule  = "forwardingRule"

	customResponseHeadersField = "customResponseHeaders"
)

const (
	tokenGlobalAddress        = "gcp:compute/globalAddress:GlobalAddress"
	tokenBackendService       = "gcp:compute/backendService:BackendService"
	tokenBackendBucket        = "gcp:compute/backendBucket:BackendBucket"
	tokenCertificateMap       = "gcp:certificatemanager/certificateMap:CertificateMap"
	tokenCertificateMapEntry  = "gcp:certificatemanager/certificateMapEntry:CertificateMapEntry"
	tokenURLMap               = "gcp:compute/uRLMap:URLMap"
	tokenTargetHTTPSProxy     = "gcp:compute/targetHttpsProxy:TargetHttpsProxy"
	tokenGlobalForwardingRule = "gcp:compute/globalForwardingRule:GlobalForwardingRule"
	tokenNetworkEndpointGroup = "gcp:compute/regionNetworkEndpointGroup:RegionNetworkEndpointGroup"
)

var (
	EdgeSurfaces         = []string{surfaceBackendService, surfaceCertificateMapEntry, surfaceNEG}
	LoadBalancerSurfaces = []string{
		surfaceAddress, surfaceAssetsBackend, surfaceCertificateMap, surfaceForwardingRule,
		surfaceHTTPSProxy, surfaceNotFoundBackend, surfaceURLMap,
	}
)

var ownedFields = map[string][]string{
	surfaceBackendService: {
		"backends", "cdnPolicy.bypassCacheOnRequestHeaders", "cdnPolicy.cacheKeyPolicy", "cdnPolicy.cacheMode",
		"deletionPolicy", "enableCdn", "externalManagedMigrationState", "externalManagedMigrationTestingPercentage",
		"loadBalancingScheme", "name", "params", "project", "protocol",
	},
	surfaceNEG: {
		"appEngine", "cloudFunction", "cloudRun", "deletionPolicy", "name", "network", "networkEndpointType",
		"project", "pscData", "pscTargetService", "region", "serverlessDeployment", "subnetwork",
	},
	surfaceCertificateMapEntry: {"certificates", "deletionPolicy", "hostname", "labels", "map", "matcher", "name", "project"},
	surfaceAddress: {
		"address", "addressType", "deletionPolicy", "ipVersion", "labels", "name", "network", "prefixLength",
		"project", "purpose",
	},
	surfaceNotFoundBackend: {
		"backends", "cdnPolicy", "deletionPolicy", "enableCdn", "externalManagedMigrationState",
		"externalManagedMigrationTestingPercentage", "loadBalancingScheme", "name", "params", "project", "protocol",
	},
	surfaceAssetsBackend: {
		"bucketName", "cdnPolicy.cacheMode", "cdnPolicy.negativeCaching", "deletionPolicy", "enableCdn",
		"loadBalancingScheme", "name", "params", "project",
	},
	surfaceCertificateMap: {"deletionPolicy", "labels", "name", "project"},
	surfaceURLMap: {
		"defaultRouteAction", "defaultService", "defaultUrlRedirect", "deletionPolicy", "headerAction", "hostRules",
		"name", "pathMatchers", "project", "tests",
	},
	surfaceHTTPSProxy: {
		"certificateManagerCertificates", "certificateMap", "deletionPolicy", "name", "project", "proxyBind",
		"sslCertificates", "urlMap",
	},
	surfaceForwardingRule: {
		"allowPscGlobalAccess", "deletionPolicy", "externalManagedBackendBucketMigrationState",
		"externalManagedBackendBucketMigrationTestingPercentage", "ipAddress", "ipProtocol", "ipVersion", "labels",
		"loadBalancingScheme", "metadataFilters", "name", "network", "networkTier", "noAutomateDnsZone", "portRange",
		"project", "serviceDirectoryRegistrations", "sourceIpRanges", "subnetwork", "target",
	},
}

func frontRefs(names names) map[string]ocelpulumi.ResourceRef {
	return map[string]ocelpulumi.ResourceRef{
		surfaceAddress:         {Token: tokenGlobalAddress, Name: names.Address},
		surfaceNotFoundBackend: {Token: tokenBackendService, Name: names.NotFound},
		surfaceAssetsBackend:   {Token: tokenBackendBucket, Name: names.AssetsBackend},
		surfaceCertificateMap:  {Token: tokenCertificateMap, Name: names.CertificateMap},
		surfaceURLMap:          {Token: tokenURLMap, Name: names.URLMap},
		surfaceHTTPSProxy:      {Token: tokenTargetHTTPSProxy, Name: names.Proxy},
		surfaceForwardingRule:  {Token: tokenGlobalForwardingRule, Name: names.Rule},
	}
}

func EvaluateFront(ctx context.Context, pass transform.Pass, tier environment.Tier) (transform.Result, error) {
	if pass == nil {
		return transform.Result{}, nil
	}
	req := surface.Request(tier, "", []transform.Resource{{Type: TransformKindLoadBalancer, Name: string(tier)}})
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
	if err := refuseUnpatchable(named, results[0], LoadBalancerSurfaces); err != nil {
		return transform.Result{}, err
	}
	if _, _, err := takeHeaders(results[0].Patches[surfaceAssetsBackend], named+"'s "+surfaceAssetsBackend); err != nil {
		return transform.Result{}, err
	}
	return results[0], nil
}

func EvaluateApp(ctx context.Context, pass transform.Pass, spec provider.StackSpec) (transform.Result, error) {
	if pass == nil || spec.App == nil || spec.Edge == nil || spec.Edge.Kind() != Kind {
		return transform.Result{}, nil
	}
	app := spec.App.App
	req := surface.Request(spec.Ref.Tier, "", []transform.Resource{{Type: TransformKindEdge, Name: app, App: app}})
	results, err := evaluateOne(ctx, pass, req)
	if err != nil || results == nil {
		return transform.Result{}, err
	}
	if err := refuseUnpatchable(TransformKindEdge+" "+app, results[0], EdgeSurfaces); err != nil {
		return transform.Result{}, err
	}
	if err := transform.ResolveOutputs(ctx, spec, req, results); err != nil {
		return transform.Result{}, err
	}
	if _, _, err := takeHeaders(results[0].Patches[surfaceBackendService], TransformKindEdge+" "+app+"'s "+surfaceBackendService); err != nil {
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
				named, key, Kind, strings.Join(constructed, ", "))
		}
	}
	for _, key := range slices.Sorted(maps.Keys(result.Patches)) {
		if err := transform.RefuseOwned(named+"'s "+key, result.Patches[key], ownedFields[key]); err != nil {
			return err
		}
	}
	return surface.RefuseInvalidLabels(named, result.Tags)
}

type frontPatches struct {
	applied       *ocelpulumi.Patches
	assetsHeaders []string
	labels        pulumi.StringMap
}

func newFrontPatches(names names, tier environment.Tier, result transform.Result) (frontPatches, error) {
	patches := result.Patches
	site := func(key string) string { return "the " + string(tier) + " tier's load balancer's " + key }
	assets, headers, err := takeHeaders(patches[surfaceAssetsBackend], site(surfaceAssetsBackend))
	if err != nil {
		return frontPatches{}, err
	}
	applied := ocelpulumi.NewPatches()
	refs := frontRefs(names)
	for _, key := range slices.Sorted(maps.Keys(patches)) {
		patch := patches[key]
		if key == surfaceAssetsBackend {
			patch = assets
		}
		ref, constructed := refs[key]
		if !constructed {
			return frontPatches{}, refusal.Refuse(refusal.CodeInvalid,
				"a transform patches %s, and the %s edge constructs no such resource", site(key), Kind)
		}
		if len(patch) == 0 {
			continue
		}
		if err := applied.Add(ref, site(key), patch); err != nil {
			return frontPatches{}, err
		}
	}
	return frontPatches{applied: applied, assetsHeaders: headers, labels: labelsOf(result.Tags)}, nil
}

type appPatches struct {
	applied *ocelpulumi.Patches
	headers map[string][]string
	labels  map[string]pulumi.StringMap
}

func (a appPatches) headersOf(app string) []string { return a.headers[app] }

func (a appPatches) labelsOf(app string) pulumi.StringMap { return a.labels[app] }

func newAppPatches(spec bindingSpec) (appPatches, error) {
	built := appPatches{applied: ocelpulumi.NewPatches(), headers: map[string][]string{}, labels: map[string]pulumi.StringMap{}}
	for _, app := range slices.Sorted(maps.Keys(spec.Patches)) {
		result := spec.Patches[app]
		site := func(key string) string { return TransformKindEdge + " " + app + "'s " + key }
		backend, headers, err := takeHeaders(result.Patches[surfaceBackendService], site(surfaceBackendService))
		if err != nil {
			return appPatches{}, err
		}
		built.headers[app] = headers
		built.labels[app] = labelsOf(result.Tags)
		patches := transform.Patches{}
		maps.Copy(patches, result.Patches)
		patches[surfaceBackendService] = backend
		for _, hostname := range slices.Sorted(maps.Keys(spec.Hosts)) {
			host := spec.Hosts[hostname]
			if host.App != app {
				continue
			}
			refs, err := hostRefs(spec, hostname, host)
			if err != nil {
				return appPatches{}, err
			}
			for _, key := range slices.Sorted(maps.Keys(patches)) {
				ref, constructed := refs[key]
				if !constructed || len(patches[key]) == 0 {
					continue
				}
				if err := built.applied.Add(ref, site(key), patches[key]); err != nil {
					return appPatches{}, err
				}
			}
		}
	}
	return built, nil
}

func hostRefs(spec bindingSpec, hostname string, host Host) (map[string]ocelpulumi.ResourceRef, error) {
	refs := map[string]ocelpulumi.ResourceRef{}
	if host.Certificate != "" {
		entry, err := entryName(spec.Namespace, spec.Slug, spec.Tier, hostname)
		if err != nil {
			return nil, err
		}
		refs[surfaceCertificateMapEntry] = ocelpulumi.ResourceRef{Token: tokenCertificateMapEntry, Name: entry}
	}
	if host.Service != "" {
		neg, err := negName(spec.Namespace, spec.Slug, spec.Tier, hostname)
		if err != nil {
			return nil, err
		}
		refs[surfaceNEG] = ocelpulumi.ResourceRef{Token: tokenNetworkEndpointGroup, Name: neg}
		refs[surfaceBackendService] = ocelpulumi.ResourceRef{Token: tokenBackendService, Name: host.Backend}
	}
	return refs, nil
}

func takeHeaders(patch map[string]any, site string) (map[string]any, []string, error) {
	listed, set := patch[customResponseHeadersField]
	if !set {
		return patch, nil, nil
	}
	values, isList := listed.([]any)
	if !isList {
		return nil, nil, refuseHeaders(site, listed)
	}
	headers := make([]string, 0, len(values))
	for _, value := range values {
		header, isString := value.(string)
		if !isString {
			return nil, nil, refuseHeaders(site, value)
		}
		headers = append(headers, header)
	}
	rest := maps.Clone(patch)
	delete(rest, customResponseHeadersField)
	return rest, headers, nil
}

func refuseHeaders(site string, value any) error {
	return refusal.Refuse(refusal.CodeInvalid,
		"a transform gives %s.%s %v, and it takes a list of headers such as \"Strict-Transport-Security: max-age=31536000\"",
		site, customResponseHeadersField, value)
}

func responseHeaders(tier environment.Tier, added []string) pulumi.StringArray {
	headers := responseHeadersOf(tier)
	for _, header := range added {
		headers = append(headers, pulumi.String(header))
	}
	return headers
}

func labelsOf(tags map[string]string) pulumi.StringMap {
	if len(tags) == 0 {
		return nil
	}
	return pulumi.ToStringMap(tags)
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

func (a *AppPatches) Read(tier environment.Tier, slug string) map[string]transform.Result {
	a.mu.Lock()
	defer a.mu.Unlock()
	read := map[string]transform.Result{}
	for key, result := range a.results {
		if key.tier == tier && key.slug == slug {
			read[key.app] = result
		}
	}
	return read
}

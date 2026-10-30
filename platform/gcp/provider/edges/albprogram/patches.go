package albprogram

import (
	"maps"
	"slices"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/environment"
	ocelpulumi "github.com/ocelhq/ocel/pkg/provider/pulumi"
	"github.com/ocelhq/ocel/pkg/provider/transform"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/albresources"
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

func frontRefs(names albresources.Names) map[string]ocelpulumi.ResourceRef {
	return map[string]ocelpulumi.ResourceRef{
		albresources.SurfaceAddress:         {Token: tokenGlobalAddress, Name: names.Address},
		albresources.SurfaceNotFoundBackend: {Token: tokenBackendService, Name: names.NotFound},
		albresources.SurfaceAssetsBackend:   {Token: tokenBackendBucket, Name: names.AssetsBackend},
		albresources.SurfaceCertificateMap:  {Token: tokenCertificateMap, Name: names.CertificateMap},
		albresources.SurfaceURLMap:          {Token: tokenURLMap, Name: names.URLMap},
		albresources.SurfaceHTTPSProxy:      {Token: tokenTargetHTTPSProxy, Name: names.Proxy},
		albresources.SurfaceForwardingRule:  {Token: tokenGlobalForwardingRule, Name: names.Rule},
	}
}

type frontPatches struct {
	applied       *ocelpulumi.Patches
	assetsHeaders []string
	labels        pulumi.StringMap
}

func newFrontPatches(names albresources.Names, tier environment.Tier, result transform.Result) (frontPatches, error) {
	patches := result.Patches
	site := func(key string) string { return "the " + string(tier) + " tier's load balancer's " + key }
	assets, headers, err := albresources.TakeHeaders(patches[albresources.SurfaceAssetsBackend], site(albresources.SurfaceAssetsBackend))
	if err != nil {
		return frontPatches{}, err
	}
	applied := ocelpulumi.NewPatches()
	refs := frontRefs(names)
	for _, key := range slices.Sorted(maps.Keys(patches)) {
		patch := patches[key]
		if key == albresources.SurfaceAssetsBackend {
			patch = assets
		}
		ref, constructed := refs[key]
		if !constructed {
			return frontPatches{}, refusal.Refuse(refusal.CodeInvalid,
				"a transform patches %s, and the %s edge constructs no such resource", site(key), albresources.Kind)
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

func newAppPatches(spec albresources.BindingSpec) (appPatches, error) {
	built := appPatches{applied: ocelpulumi.NewPatches(), headers: map[string][]string{}, labels: map[string]pulumi.StringMap{}}
	for _, app := range slices.Sorted(maps.Keys(spec.Patches)) {
		result := spec.Patches[app]
		site := func(key string) string { return albresources.TransformKindEdge + " " + app + "'s " + key }
		backend, headers, err := albresources.TakeHeaders(result.Patches[albresources.SurfaceBackendService], site(albresources.SurfaceBackendService))
		if err != nil {
			return appPatches{}, err
		}
		built.headers[app] = headers
		built.labels[app] = labelsOf(result.Tags)
		patches := transform.Patches{}
		maps.Copy(patches, result.Patches)
		patches[albresources.SurfaceBackendService] = backend
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

func hostRefs(spec albresources.BindingSpec, hostname string, host albresources.BindingHost) (map[string]ocelpulumi.ResourceRef, error) {
	refs := map[string]ocelpulumi.ResourceRef{}
	if host.Certificate != "" {
		entry, err := albresources.EntryName(spec.Namespace, spec.Slug, spec.Tier, hostname)
		if err != nil {
			return nil, err
		}
		refs[albresources.SurfaceCertificateMapEntry] = ocelpulumi.ResourceRef{Token: tokenCertificateMapEntry, Name: entry}
	}
	if host.Service != "" {
		neg, err := albresources.NegName(spec.Namespace, spec.Slug, spec.Tier, hostname)
		if err != nil {
			return nil, err
		}
		refs[albresources.SurfaceNEG] = ocelpulumi.ResourceRef{Token: tokenNetworkEndpointGroup, Name: neg}
		refs[albresources.SurfaceBackendService] = ocelpulumi.ResourceRef{Token: tokenBackendService, Name: host.Backend}
	}
	return refs, nil
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

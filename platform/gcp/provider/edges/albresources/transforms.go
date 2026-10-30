package albresources

import (
	"maps"

	"github.com/ocelhq/ocel/pkg/refusal"
)

const (
	TransformKindEdge         = "edge"
	TransformKindLoadBalancer = "loadBalancer"

	SurfaceBackendService      = "backendService"
	SurfaceNEG                 = "neg"
	SurfaceCertificateMapEntry = "certificateMapEntry"

	SurfaceAddress         = "address"
	SurfaceNotFoundBackend = "notFoundBackend"
	SurfaceAssetsBackend   = "assetsBackend"
	SurfaceCertificateMap  = "certificateMap"
	SurfaceURLMap          = "urlMap"
	SurfaceHTTPSProxy      = "httpsProxy"
	SurfaceForwardingRule  = "forwardingRule"

	customResponseHeadersField = "customResponseHeaders"
)

var (
	EdgeSurfaces         = []string{SurfaceBackendService, SurfaceCertificateMapEntry, SurfaceNEG}
	LoadBalancerSurfaces = []string{
		SurfaceAddress, SurfaceAssetsBackend, SurfaceCertificateMap, SurfaceForwardingRule,
		SurfaceHTTPSProxy, SurfaceNotFoundBackend, SurfaceURLMap,
	}
)

func TakeHeaders(patch map[string]any, site string) (map[string]any, []string, error) {
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

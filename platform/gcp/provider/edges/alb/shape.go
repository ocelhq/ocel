package alb

import (
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type Shaped struct {
	Name       string
	Type       string
	Properties map[string]any
}

const (
	tfGlobalAddress        = "google_compute_global_address"
	tfBackendService       = "google_compute_backend_service"
	tfCertificateMap       = "google_certificate_manager_certificate_map"
	tfCertificateMapEntry  = "google_certificate_manager_certificate_map_entry"
	tfURLMap               = "google_compute_url_map"
	tfTargetHTTPSProxy     = "google_compute_target_https_proxy"
	tfGlobalForwardingRule = "google_compute_global_forwarding_rule"
	tfNetworkEndpointGroup = "google_compute_region_network_endpoint_group"
)

func ShapeFront(class edge.Class, previewBaseDomain string) []Shaped {
	held := frontNames(class)
	shaped := []Shaped{
		{Name: held.Address, Type: tfGlobalAddress, Properties: map[string]any{"address_type": "EXTERNAL"}},
		{Name: held.NotFound, Type: tfBackendService, Properties: backendProperties(false)},
		{Name: held.CertificateMap, Type: tfCertificateMap, Properties: map[string]any{}},
		{Name: held.URLMap, Type: tfURLMap, Properties: map[string]any{}},
		{Name: held.Proxy, Type: tfTargetHTTPSProxy, Properties: map[string]any{}},
		{Name: held.Rule, Type: tfGlobalForwardingRule, Properties: map[string]any{
			"load_balancing_scheme": externalManaged,
			"network_tier":          premiumTier,
			"port_range":            httpsPortRange,
		}},
	}
	if previewBaseDomain != "" {
		shaped = append(shaped,
			Shaped{Name: previewEntryName(previewBaseDomain), Type: tfCertificateMapEntry, Properties: map[string]any{}},
			Shaped{Name: previewNEGName(previewBaseDomain), Type: tfNetworkEndpointGroup, Properties: map[string]any{"network_endpoint_type": serverlessNEG}},
			Shaped{Name: previewBackendName(previewBaseDomain), Type: tfBackendService, Properties: backendProperties(true)},
		)
	}
	return shaped
}

func ShapeHosts(slug string, class edge.Class, hostnames []string) []Shaped {
	var shaped []Shaped
	for _, hostname := range hostnames {
		shaped = append(shaped,
			Shaped{Name: entryName(slug, class, hostname), Type: tfCertificateMapEntry, Properties: map[string]any{}},
			Shaped{Name: negName(slug, class, hostname), Type: tfNetworkEndpointGroup, Properties: map[string]any{"network_endpoint_type": serverlessNEG}},
			Shaped{Name: backendName(slug, class, hostname), Type: tfBackendService, Properties: backendProperties(true)},
		)
	}
	return shaped
}

func backendProperties(cdn bool) map[string]any {
	return map[string]any{
		"load_balancing_scheme": externalManaged,
		"protocol":              "HTTPS",
		"enable_cdn":            cdn,
	}
}

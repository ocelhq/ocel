package alb

import (
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/pricing"
)

const (
	costVendor        = "gcp"
	previewBaseShaped = "preview"
)

func Shape(site pricing.EdgeSite) (pricing.EdgeShape, error) {
	previewBase := ""
	if site.Class == edge.ClassPreview {
		previewBase = previewBaseShaped
	}
	shape := pricing.EdgeShape{
		Vendor:      costVendor,
		Region:      site.Region,
		BillsEgress: true,
		Shared:      ShapeFront(site.Class, previewBase),
		Apps:        make(map[string][]pricing.Shaped, len(site.Apps)),
	}
	for _, app := range site.Apps {
		if hosts := ShapeHosts(site.Slug, site.Class, app.Hostnames); len(hosts) > 0 {
			shape.Apps[app.Name] = hosts
		}
	}
	return shape, nil
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

func ShapeFront(class edge.Class, previewBaseDomain string) []pricing.Shaped {
	front := frontNames(class)
	shaped := []pricing.Shaped{
		{Name: front.Address, Type: tfGlobalAddress, Properties: map[string]any{"address_type": "EXTERNAL"}},
		{Name: front.NotFound, Type: tfBackendService, Properties: backendProperties(false)},
		{Name: front.CertificateMap, Type: tfCertificateMap, Properties: map[string]any{}},
		{Name: front.URLMap, Type: tfURLMap, Properties: map[string]any{}},
		{Name: front.Proxy, Type: tfTargetHTTPSProxy, Properties: map[string]any{}},
		{Name: front.Rule, Type: tfGlobalForwardingRule, Properties: map[string]any{
			"load_balancing_scheme": externalManaged,
			"network_tier":          premiumTier,
			"port_range":            httpsPortRange,
		}},
	}
	if previewBaseDomain != "" {
		shaped = append(shaped,
			pricing.Shaped{Name: previewEntryName(previewBaseDomain), Type: tfCertificateMapEntry, Properties: map[string]any{}},
			pricing.Shaped{Name: previewNEGName(previewBaseDomain), Type: tfNetworkEndpointGroup, Properties: map[string]any{"network_endpoint_type": serverlessNEG}},
			pricing.Shaped{Name: previewBackendName(previewBaseDomain), Type: tfBackendService, Properties: backendProperties(true)},
		)
	}
	return shaped
}

func ShapeHosts(slug string, class edge.Class, hostnames []string) []pricing.Shaped {
	var shaped []pricing.Shaped
	for _, hostname := range hostnames {
		shaped = append(shaped,
			pricing.Shaped{Name: entryName(slug, class, hostname), Type: tfCertificateMapEntry, Properties: map[string]any{}},
			pricing.Shaped{Name: negName(slug, class, hostname), Type: tfNetworkEndpointGroup, Properties: map[string]any{"network_endpoint_type": serverlessNEG}},
			pricing.Shaped{Name: backendName(slug, class, hostname), Type: tfBackendService, Properties: backendProperties(true)},
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

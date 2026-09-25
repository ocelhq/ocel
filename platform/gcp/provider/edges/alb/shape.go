package alb

import (
	"github.com/ocelhq/ocel/pkg/costkit"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	costVendor        = "gcp"
	previewBaseShaped = "preview"
)

var _ costkit.EdgeCost = (*Edge)(nil)

func (e *Edge) Shape(site costkit.EdgeSite) (costkit.EdgeShape, error) {
	previewBase := ""
	if site.Class == edge.ClassPreview {
		previewBase = previewBaseShaped
	}
	shape := costkit.EdgeShape{
		Vendor:      costVendor,
		Region:      site.Region,
		BillsEgress: true,
		Shared:      ShapeFront(site.Class, previewBase),
		Apps:        make(map[string][]costkit.Shaped, len(site.Apps)),
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

func ShapeFront(class edge.Class, previewBaseDomain string) []costkit.Shaped {
	held := frontNames(class)
	shaped := []costkit.Shaped{
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
			costkit.Shaped{Name: previewEntryName(previewBaseDomain), Type: tfCertificateMapEntry, Properties: map[string]any{}},
			costkit.Shaped{Name: previewNEGName(previewBaseDomain), Type: tfNetworkEndpointGroup, Properties: map[string]any{"network_endpoint_type": serverlessNEG}},
			costkit.Shaped{Name: previewBackendName(previewBaseDomain), Type: tfBackendService, Properties: backendProperties(true)},
		)
	}
	return shaped
}

func ShapeHosts(slug string, class edge.Class, hostnames []string) []costkit.Shaped {
	var shaped []costkit.Shaped
	for _, hostname := range hostnames {
		shaped = append(shaped,
			costkit.Shaped{Name: entryName(slug, class, hostname), Type: tfCertificateMapEntry, Properties: map[string]any{}},
			costkit.Shaped{Name: negName(slug, class, hostname), Type: tfNetworkEndpointGroup, Properties: map[string]any{"network_endpoint_type": serverlessNEG}},
			costkit.Shaped{Name: backendName(slug, class, hostname), Type: tfBackendService, Properties: backendProperties(true)},
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

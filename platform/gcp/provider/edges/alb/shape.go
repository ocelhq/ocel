package alb

import (
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/pricing"
)

const (
	costVendor        = "gcp"
	previewBaseShaped = "preview"
)

func Shape(namespace string, site pricing.EdgeSite) (pricing.EdgeShape, error) {
	previewBase := ""
	if site.Tier == environment.TierPreview {
		previewBase = previewBaseShaped
	}
	shape := pricing.EdgeShape{
		Vendor:      costVendor,
		Region:      site.Region,
		BillsEgress: true,
		Shared:      ShapeLoadBalancer(namespace, site.Tier, previewBase),
		Apps:        make(map[string][]pricing.Shaped, len(site.Apps)),
	}
	for _, app := range site.Apps {
		if hosts := ShapeHosts(namespace, site.Slug, site.Tier, app.Hostnames); len(hosts) > 0 {
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
	tfBackendBucket        = "google_compute_backend_bucket"
	tfProjectIAMMember     = "google_project_iam_member"
	tfWasmPlugin           = "google_network_services_wasm_plugin"
	tfLBEdgeExtension      = "google_network_services_lb_edge_extension"
)

func ShapeLoadBalancer(namespace string, tier environment.Tier, previewBaseDomain string) []pricing.Shaped {
	balancer := loadBalancerNames(namespace, tier, false)
	shaped := []pricing.Shaped{
		{Name: balancer.Address, Type: tfGlobalAddress, Properties: map[string]any{"address_type": "EXTERNAL"}},
		{Name: balancer.NotFound, Type: tfBackendService, Properties: backendProperties(false)},
		{Name: balancer.AssetsBackend, Type: tfBackendBucket, Properties: map[string]any{"enable_cdn": true}},
		{Name: balancer.AssetsReader, Type: tfProjectIAMMember, Properties: map[string]any{"role": objectViewerRole}},
		{Name: balancer.CertificateMap, Type: tfCertificateMap, Properties: map[string]any{}},
		{Name: balancer.URLMap, Type: tfURLMap, Properties: map[string]any{}},
		{Name: balancer.Proxy, Type: tfTargetHTTPSProxy, Properties: map[string]any{}},
		{Name: balancer.Rule, Type: tfGlobalForwardingRule, Properties: map[string]any{
			"load_balancing_scheme": externalManaged,
			"network_tier":          premiumTier,
			"port_range":            httpsPortRange,
		}},
		{Name: balancer.GatePlugin, Type: tfWasmPlugin, Properties: map[string]any{}},
		{Name: balancer.GateExtension, Type: tfLBEdgeExtension, Properties: map[string]any{"load_balancing_scheme": externalManaged}},
	}
	if previewBaseDomain != "" {
		shaped = append(shaped,
			pricing.Shaped{Name: previewEntryName(namespace, previewBaseDomain), Type: tfCertificateMapEntry, Properties: map[string]any{}},
		)
	}
	return shaped
}

func ShapeHosts(namespace, slug string, tier environment.Tier, hostnames []string) []pricing.Shaped {
	var shaped []pricing.Shaped
	for _, hostname := range hostnames {
		shaped = append(shaped,
			pricing.Shaped{Name: entryName(namespace, slug, tier, hostname), Type: tfCertificateMapEntry, Properties: map[string]any{}},
			pricing.Shaped{Name: negName(namespace, slug, tier, hostname), Type: tfNetworkEndpointGroup, Properties: map[string]any{"network_endpoint_type": serverlessNEG}},
			pricing.Shaped{Name: backendName(namespace, slug, tier, hostname), Type: tfBackendService, Properties: backendProperties(true)},
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

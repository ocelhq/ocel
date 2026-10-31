package alb

import (
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/pricing"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/albresources"
)

const (
	costVendor      = "gcp"
	previewBaseName = "preview"
)

func Inventory(namespace string, site pricing.EdgeSite) (pricing.EdgeInventory, error) {
	shared := InventoryLoadBalancer(namespace, site.Tier)
	if site.Tier == environment.TierPreview {
		entry, err := albresources.PreviewEntryName(namespace, previewBaseName)
		if err != nil {
			return pricing.EdgeInventory{}, err
		}
		shared = append(shared, pricing.Resource{Name: entry, Type: tfCertificateMapEntry, Properties: map[string]any{}})
	}
	inventory := pricing.EdgeInventory{
		Vendor:      costVendor,
		Region:      site.Region,
		BillsEgress: true,
		Shared:      shared,
		Apps:        make(map[string][]pricing.Resource, len(site.Apps)),
	}
	for _, app := range site.Apps {
		hosts, err := InventoryHosts(namespace, site.Slug, site.Tier, app.Hostnames)
		if err != nil {
			return pricing.EdgeInventory{}, err
		}
		if len(hosts) > 0 {
			inventory.Apps[app.Name] = hosts
		}
	}
	return inventory, nil
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

func InventoryLoadBalancer(namespace string, tier environment.Tier) []pricing.Resource {
	balancer := albresources.LoadBalancerNames(namespace, tier)
	inventory := []pricing.Resource{
		{Name: balancer.Address, Type: tfGlobalAddress, Properties: map[string]any{"address_type": "EXTERNAL"}},
		{Name: balancer.NotFound, Type: tfBackendService, Properties: backendProperties(false)},
		{Name: balancer.AssetsBackend, Type: tfBackendBucket, Properties: map[string]any{"enable_cdn": true}},
		{Name: balancer.AssetsReader, Type: tfProjectIAMMember, Properties: map[string]any{"role": albresources.ObjectViewerRole}},
		{Name: balancer.CertificateMap, Type: tfCertificateMap, Properties: map[string]any{}},
		{Name: balancer.URLMap, Type: tfURLMap, Properties: map[string]any{}},
		{Name: balancer.Proxy, Type: tfTargetHTTPSProxy, Properties: map[string]any{}},
		{Name: balancer.Rule, Type: tfGlobalForwardingRule, Properties: map[string]any{
			"load_balancing_scheme": albresources.ExternalManaged,
			"network_tier":          albresources.PremiumTier,
			"port_range":            albresources.HTTPSPortRange,
		}},
		{Name: balancer.GatePlugin, Type: tfWasmPlugin, Properties: map[string]any{}},
		{Name: balancer.GateExtension, Type: tfLBEdgeExtension, Properties: map[string]any{"load_balancing_scheme": albresources.ExternalManaged}},
	}
	return inventory
}

func InventoryHosts(namespace, slug string, tier environment.Tier, hostnames []string) ([]pricing.Resource, error) {
	var inventory []pricing.Resource
	for _, hostname := range hostnames {
		entry, err := albresources.EntryName(namespace, slug, tier, hostname)
		if err != nil {
			return nil, err
		}
		neg, err := albresources.NegName(namespace, slug, tier, hostname)
		if err != nil {
			return nil, err
		}
		backend, err := albresources.BackendName(namespace, slug, tier, hostname)
		if err != nil {
			return nil, err
		}
		inventory = append(inventory,
			pricing.Resource{Name: entry, Type: tfCertificateMapEntry, Properties: map[string]any{}},
			pricing.Resource{Name: neg, Type: tfNetworkEndpointGroup, Properties: map[string]any{"network_endpoint_type": albresources.ServerlessNEG}},
			pricing.Resource{Name: backend, Type: tfBackendService, Properties: backendProperties(true)},
		)
	}
	return inventory, nil
}

func backendProperties(cdn bool) map[string]any {
	return map[string]any{
		"load_balancing_scheme": albresources.ExternalManaged,
		"protocol":              "HTTPS",
		"enable_cdn":            cdn,
	}
}
